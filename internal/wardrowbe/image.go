package wardrowbe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// maxImageReadBytes caps how many bytes we buffer from an image response, and
// maxImagePixels bounds the decoded pixel count so a small but highly compressed
// image (a "decompression bomb") cannot expand into a multi-GB allocation.
const (
	maxImageReadBytes = 20 << 20   // 20 MiB on the wire
	maxImagePixels    = 24_000_000 // ~24 MP decoded (e.g. 6000x4000)
)

// ImageData is a fetched garment photo ready to hand to the MCP layer.
type ImageData struct {
	ItemID string
	Data   []byte
	MIME   string
}

// variantFields maps a logical variant to the item payload keys that locate it.
var variantFields = map[string]struct{ urlKey, pathKey string }{
	"thumb":  {"thumbnail_url", "thumbnail_path"},
	"medium": {"medium_url", "medium_path"},
	"full":   {"image_url", "image_path"},
}

// ItemImage fetches a single item's photo at the requested variant, sniffs its
// MIME type, and downscales it to maxDim (longest edge) when feasible.
// variant is one of thumb|medium|full; an unknown variant falls back to medium.
func (c *Client) ItemImage(ctx context.Context, itemID, variant string, maxDim int) (ImageData, error) {
	raw, err := c.Request(ctx, http.MethodGet, "/items/"+url.PathEscape(itemID), nil, nil)
	if err != nil {
		return ImageData{}, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ImageData{}, fmt.Errorf("decode item %s: %w", itemID, err)
	}
	return c.ItemImageFromPayload(ctx, itemID, fields, variant, maxDim)
}

// ItemImageFromPayload resolves and fetches an image given an already-decoded
// item (or outfit garment) payload, avoiding a second GET /items/{id}. Used for
// garments embedded in an outfit response.
func (c *Client) ItemImageFromPayload(ctx context.Context, itemID string, fields map[string]any, variant string, maxDim int) (ImageData, error) {
	keys, ok := variantFields[variant]
	if !ok {
		keys = variantFields["medium"]
	}

	imgURL, authed, err := c.resolveImageURL(fields, keys)
	if err != nil {
		return ImageData{}, fmt.Errorf("item %s: %w", itemID, err)
	}

	data, mime, err := c.fetchImageBytes(ctx, imgURL, authed)
	if err != nil {
		return ImageData{}, fmt.Errorf("item %s: %w", itemID, err)
	}

	data, mime = downscale(data, mime, maxDim)
	return ImageData{ItemID: itemID, Data: data, MIME: mime}, nil
}

// resolveImageURL builds the URL for a variant and reports whether a bearer
// token should be attached. A pre-signed absolute URL is fetched as-is; a
// relative URL or a bare stored path is served from the backend with auth.
func (c *Client) resolveImageURL(fields map[string]any, keys struct{ urlKey, pathKey string }) (string, bool, error) {
	if u := StringField(fields, keys.urlKey); u != "" {
		if isAbsoluteURL(u) {
			return u, false, nil // pre-signed; transport already authenticates
		}
		return c.baseURL + ensureLeadingSlash(u), true, nil
	}

	if p := StringField(fields, keys.pathKey); p != "" {
		userID := StringField(fields, "user_id")
		if userID == "" {
			return "", false, fmt.Errorf("missing user_id for image path %q", p)
		}
		filename := path.Base(p)
		return c.baseURL + apiBase + "/images/" + url.PathEscape(userID) + "/" + url.PathEscape(filename), true, nil
	}

	return "", false, fmt.Errorf("no image url or path available for this variant")
}

func (c *Client) fetchImageBytes(ctx context.Context, imageURL string, authed bool) ([]byte, string, error) {
	// Mirror execute()'s 401 handling: re-sync the token once and retry, so an
	// image fetch racing token expiry doesn't fail where a JSON call would not.
	badToken := ""
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("build image request: %w", err)
		}
		token := ""
		if authed {
			token, err = c.tokenFor(ctx, badToken)
			if err != nil {
				return nil, "", err
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			c.log.Debug("image fetch request failed", "err", err)
			return nil, "", fmt.Errorf("fetch image: request failed")
		}
		if authed && resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			// Drain a little of the body so the connection can be reused, then
			// retry with a fresh token; the drain/close errors are irrelevant.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
			_ = resp.Body.Close()
			c.log.Debug("image fetch 401, re-syncing token")
			badToken = token
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Read only a small slice of an error body for diagnostics.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			c.log.Debug("image fetch failed", "status", resp.StatusCode, "body", string(body))
			return nil, "", &APIError{StatusCode: resp.StatusCode, Method: http.MethodGet, Path: "image", Body: string(body)}
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageReadBytes+1))
		if err != nil {
			return nil, "", fmt.Errorf("read image bytes: %w", err)
		}
		if len(data) > maxImageReadBytes {
			return nil, "", fmt.Errorf("image exceeds %d MiB limit", maxImageReadBytes>>20)
		}

		mime := resp.Header.Get("Content-Type")
		if mime == "" || !strings.HasPrefix(mime, "image/") {
			mime = http.DetectContentType(data)
		}
		return data, mime, nil
	}
}

// downscale shrinks JPEG/PNG images whose longest edge exceeds maxDim, returning
// re-encoded bytes. Unsupported formats or already-small images are returned
// unchanged. Downscaling is best-effort: any decode/encode error yields the
// original bytes.
func downscale(data []byte, mime string, maxDim int) ([]byte, string) {
	if maxDim <= 0 {
		return data, mime
	}
	switch mime {
	case "image/jpeg", "image/png":
	default:
		return data, mime
	}

	// Read dimensions cheaply first; refuse to fully decode a decompression bomb
	// whose pixel count would blow up memory. Returning the original bytes is
	// safe — they were already byte-size-capped upstream.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return data, mime
	}
	// Multiply in int64: on 32-bit platforms (the released armv7 binary) a
	// crafted header like 46341x46341 overflows native int to a negative
	// product and would slip past the cap.
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return data, mime
	}

	// Re-encoding strips EXIF, including the Orientation tag — a rotated phone
	// JPEG would come back sideways. Keep the original bytes (already
	// byte-size-capped) for anything not stored upright.
	if mime == "image/jpeg" && jpegOrientation(data) != 1 {
		return data, mime
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mime
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return data, mime
	}

	nw, nh := scaledDims(w, h, maxDim)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)

	var buf bytes.Buffer
	if mime == "image/png" {
		if err := png.Encode(&buf, dst); err != nil {
			return data, mime
		}
		return buf.Bytes(), "image/png"
	}
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return data, mime
	}
	return buf.Bytes(), "image/jpeg"
}

// jpegOrientation returns the EXIF Orientation tag (1–8) of a JPEG, or 1 when
// the tag is absent or anything fails to parse (1 = stored upright). It walks
// JPEG segments to the APP1/Exif block and reads tag 0x0112 from IFD0 — just
// enough EXIF to decide whether re-encoding would lose a rotation.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		switch {
		case marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			i += 2 // standalone marker, no length
			continue
		case marker == 0xDA:
			return 1 // start of scan: no APP1 ahead of the image data
		}
		size := int(data[i+2])<<8 | int(data[i+3])
		if size < 2 || i+2+size > len(data) {
			return 1
		}
		if marker == 0xE1 {
			if o := exifOrientation(data[i+4 : i+2+size]); o != 0 {
				return o
			}
		}
		i += 2 + size
	}
	return 1
}

// exifOrientation extracts Orientation from an APP1/Exif payload, returning 0
// when absent or malformed.
func exifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	tiff := seg[6:]
	var be bool
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		be = false
	case tiff[0] == 'M' && tiff[1] == 'M':
		be = true
	default:
		return 0
	}
	u16 := func(b []byte) int {
		if be {
			return int(b[0])<<8 | int(b[1])
		}
		return int(b[1])<<8 | int(b[0])
	}
	u32 := func(b []byte) int {
		if be {
			return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		}
		return int(b[3])<<24 | int(b[2])<<16 | int(b[1])<<8 | int(b[0])
	}
	if u16(tiff[2:4]) != 42 {
		return 0
	}
	off := u32(tiff[4:8])
	if off < 0 || off+2 > len(tiff) {
		return 0
	}
	n := u16(tiff[off : off+2])
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(tiff) {
			return 0
		}
		if u16(tiff[e:e+2]) == 0x0112 { // Orientation
			if v := u16(tiff[e+8 : e+10]); v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}

func scaledDims(w, h, maxDim int) (int, int) {
	if w >= h {
		return maxDim, max(1, h*maxDim/w)
	}
	return max(1, w*maxDim/h), maxDim
}

// StringField returns the trimmed string value at key in a decoded JSON object,
// or "" if absent or not a string. Shared by the backend client and the MCP
// layer for reading fields out of raw item/outfit payloads.
func StringField(fields map[string]any, key string) string {
	if v, ok := fields[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func isAbsoluteURL(u string) bool {
	parsed, err := url.Parse(u)
	return err == nil && parsed.IsAbs()
}

func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}
