package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	// maxImageBytes caps a fetched image (the backend ingress allows ~50 MB).
	maxImageBytes = 20 << 20 // 20 MiB
	// imageFetchTimeout bounds the whole external fetch.
	imageFetchTimeout = 30 * time.Second
	// browserUA avoids naive hotlink/User-Agent blocks on retail CDNs.
	browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/124.0 Safari/537.36 wardrowbe-mcp"
)

func (s *Server) registerCreateTools() {
	s.add(mcp.NewTool("create_item_from_url",
		mcp.WithDescription("Create a wardrobe item from a public image URL. The server downloads the "+
			"image and uploads it to Wardrowbe (the backend then auto-tags it). Use this to add a "+
			"garment from a product/photo link; afterwards refine attributes with get_item_image + "+
			"set_item_tags/set_item_description. Note: only http(s) URLs to public hosts are allowed, "+
			"and Claude cannot upload an image pasted into the chat — pass a URL. If the photo is only "+
			"on local disk, either use create_item_from_base64, or upload it to a temporary public host "+
			"such as litterbox.catbox.moe (POST reqtype=fileupload, time=1h, fileToUpload=@photo to "+
			"https://litterbox.catbox.moe/resources/internals/api.php) and pass the returned 1-hour URL."),
		mcp.WithString("image_url", mcp.Required(), mcp.Description("Public http(s) URL of the garment image.")),
		mcp.WithString("name", mcp.Description("Optional item name.")),
		mcp.WithString("type", mcp.Description("Optional item type (e.g. shirt, pants, jacket).")),
		mcp.WithString("subtype", mcp.Description("Optional subtype.")),
		mcp.WithString("brand", mcp.Description("Optional brand.")),
		mcp.WithString("notes", mcp.Description("Optional free-text notes/description.")),
		mcp.WithString("primary_color", mcp.Description("Optional primary color.")),
		mcp.WithBoolean("favorite", mcp.Description("Mark as favorite. Default false.")),
	), s.handleCreateItemFromURL)

	s.add(mcp.NewTool("create_item_from_base64",
		mcp.WithDescription("Create a wardrobe item from a base64-encoded image supplied inline. Use this "+
			"when the image lives on the local machine (read the file and pass its bytes as base64) and "+
			"there is no public URL to give create_item_from_url. The backend auto-tags the uploaded "+
			"image; afterwards refine attributes with get_item_image + set_item_tags/set_item_description. "+
			"Accepts a raw base64 string or a data URL (data:image/...;base64,...). Keep images reasonably "+
			"small — the decoded size is capped at 20 MiB and large payloads may exceed message limits."),
		mcp.WithString("image_base64", mcp.Required(), mcp.Description("Base64-encoded image bytes, or a "+
			"full data URL (data:image/png;base64,...).")),
		mcp.WithString("filename", mcp.Description("Optional original filename (used to label the upload).")),
		mcp.WithString("name", mcp.Description("Optional item name.")),
		mcp.WithString("type", mcp.Description("Optional item type (e.g. shirt, pants, jacket).")),
		mcp.WithString("subtype", mcp.Description("Optional subtype.")),
		mcp.WithString("brand", mcp.Description("Optional brand.")),
		mcp.WithString("notes", mcp.Description("Optional free-text notes/description.")),
		mcp.WithString("primary_color", mcp.Description("Optional primary color.")),
		mcp.WithBoolean("favorite", mcp.Description("Mark as favorite. Default false.")),
	), s.handleCreateItemFromBase64)
}

func (s *Server) handleCreateItemFromURL(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	imageURL, err := req.RequireString("image_url")
	if err != nil || imageURL == "" {
		return mcp.NewToolResultError("image_url is required"), nil
	}

	data, mime, filename, err := fetchExternalImage(ctx, imageURL)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("could not fetch image", err), nil
	}

	raw, err := s.client.CreateItemFromImage(ctx, data, filename, mime, itemFields(req))
	if err != nil {
		return mcp.NewToolResultErrorFromErr("create item failed", err), nil
	}
	s.log.Info("created item from url", "url", imageURL, "bytes", len(data), "mime", mime)
	return jsonText(raw), nil
}

func (s *Server) handleCreateItemFromBase64(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw64, err := req.RequireString("image_base64")
	if err != nil || raw64 == "" {
		return mcp.NewToolResultError("image_base64 is required"), nil
	}

	data, mime, err := decodeBase64Image(raw64)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("could not decode image", err), nil
	}

	filename := req.GetString("filename", "")
	if filename == "" {
		filename = "item" + extForMIME(mime)
	}

	result, err := s.client.CreateItemFromImage(ctx, data, filename, mime, itemFields(req))
	if err != nil {
		return mcp.NewToolResultErrorFromErr("create item failed", err), nil
	}
	s.log.Info("created item from base64", "filename", filename, "bytes", len(data), "mime", mime)
	return jsonText(result), nil
}

// itemFields collects the optional item attributes shared by the create tools.
func itemFields(req mcp.CallToolRequest) map[string]string {
	fields := map[string]string{}
	for _, k := range []string{"name", "type", "subtype", "brand", "notes", "primary_color"} {
		if v := req.GetString(k, ""); v != "" {
			fields[k] = v
		}
	}
	if _, ok := req.GetArguments()["favorite"]; ok {
		fields["favorite"] = boolStr(req.GetBool("favorite", false))
	}
	return fields
}

// decodeBase64Image decodes a raw base64 string or a data URL, enforces the
// image size cap, and verifies the bytes actually look like an image. It returns
// the decoded bytes and a sniffed MIME type.
func decodeBase64Image(raw string) ([]byte, string, error) {
	declaredMIME := ""
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "data:") {
		comma := strings.IndexByte(s, ',')
		if comma < 0 {
			return nil, "", fmt.Errorf("malformed data url: missing comma")
		}
		header := s[len("data:"):comma]
		if !strings.Contains(header, ";base64") {
			return nil, "", fmt.Errorf("data url must be base64-encoded")
		}
		declaredMIME = strings.TrimSpace(strings.SplitN(header, ";", 2)[0])
		s = s[comma+1:]
	}
	// Tolerate whitespace/newlines that some clients insert into base64.
	s = strings.Join(strings.Fields(s), "")

	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, "", fmt.Errorf("invalid base64: %w", err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("image is empty")
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("image exceeds %d MiB limit", maxImageBytes>>20)
	}

	mime := declaredMIME
	if !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", fmt.Errorf("decoded bytes are not an image (content-type %q)", mime)
	}
	return data, mime, nil
}

// fetchExternalImage downloads an image from a public URL with an SSRF guard
// (http(s) only, no private/loopback addresses), a size cap, and a content-type
// check. Returns the bytes, sniffed MIME, and a sensible filename.
func fetchExternalImage(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", "", fmt.Errorf("only http(s) urls are allowed (got %q)", u.Scheme)
	}

	client := &http.Client{Timeout: imageFetchTimeout, Transport: ssrfTransport()}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	httpReq.Header.Set("User-Agent", browserUA)
	httpReq.Header.Set("Accept", "image/*")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("image host returned %d", resp.StatusCode)
	}

	// Read at most maxImageBytes+1 to detect oversize without buffering it all.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("read image: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, "", "", fmt.Errorf("image exceeds %d MiB limit", maxImageBytes>>20)
	}
	if len(data) == 0 {
		return nil, "", "", fmt.Errorf("image is empty")
	}

	mime := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.TrimSpace(mime)
	if !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", "", fmt.Errorf("url is not an image (content-type %q)", mime)
	}

	return data, mime, filenameFor(u, mime), nil
}

// ssrfTransport returns an http transport whose dialer refuses to connect to
// private, loopback, link-local or unspecified addresses — checked on the IP
// actually dialed, which defeats DNS-rebinding.
func ssrfTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !isPublicIP(ip.IP) {
					return nil, fmt.Errorf("refusing to connect to non-public address %s", ip.IP)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	// Block IPv4 carrier-grade NAT (100.64.0.0/10), commonly internal.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

func filenameFor(u *url.URL, mime string) string {
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" || !strings.Contains(base, ".") {
		base = "item" + extForMIME(mime)
	}
	return base
}

func extForMIME(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".jpg"
	}
}
