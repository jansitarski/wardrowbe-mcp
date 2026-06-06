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
	if u := stringField(fields, keys.urlKey); u != "" {
		if isAbsoluteURL(u) {
			return u, false, nil // pre-signed; transport already authenticates
		}
		return c.baseURL + ensureLeadingSlash(u), true, nil
	}

	if p := stringField(fields, keys.pathKey); p != "" {
		userID := stringField(fields, "user_id")
		if userID == "" {
			return "", false, fmt.Errorf("missing user_id for image path %q", p)
		}
		filename := path.Base(p)
		return c.baseURL + apiBase + "/images/" + url.PathEscape(userID) + "/" + url.PathEscape(filename), true, nil
	}

	return "", false, fmt.Errorf("no image url or path available for this variant")
}

func (c *Client) fetchImageBytes(ctx context.Context, imageURL string, authed bool) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build image request: %w", err)
	}
	if authed {
		token, err := c.ensureToken(ctx)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read image bytes: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", &APIError{StatusCode: resp.StatusCode, Method: http.MethodGet, Path: "image", Body: string(data)}
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" || !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
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

func scaledDims(w, h, maxDim int) (int, int) {
	if w >= h {
		return maxDim, max(1, h*maxDim/w)
	}
	return max(1, w*maxDim/h), maxDim
}

func stringField(fields map[string]any, key string) string {
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
