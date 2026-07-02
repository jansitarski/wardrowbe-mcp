package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

// errNonPublicAddr is returned by the SSRF dialer when a URL resolves to a
// non-public address. It is a sentinel (no IP in the message) so the tool layer
// can surface the block reason without leaking the resolved internal IP.
var errNonPublicAddr = errors.New("refusing to connect to a non-public address")

const (
	// maxImageBytes caps a fetched image (the backend ingress allows ~50 MB).
	maxImageBytes = 20 << 20 // 20 MiB
	// imageFetchTimeout bounds the whole external fetch.
	imageFetchTimeout = 30 * time.Second
	// maxImageRedirects bounds the redirect chain. Each hop is still re-checked
	// by the SSRF dialer; this caps the chain length as defense-in-depth (a
	// legitimate CDN may 302 once to a signed URL, but not many times).
	maxImageRedirects = 5
	// browserUA avoids naive hotlink/User-Agent blocks on retail CDNs. It is kept
	// generic (no product/host identifier) so outbound fetches don't fingerprint
	// this server to every CDN it reaches.
	browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

func (s *Server) registerCreateTools() {
	s.add(mcp.NewTool("wardrowbe_create_item_from_url",
		mcp.WithDescription("Create a wardrobe item from a public image URL. The server downloads the "+
			"image and uploads it to Wardrowbe (the backend then auto-tags it). Use this to add a "+
			"garment from a product/photo link; afterwards refine attributes with wardrowbe_get_item_image + "+
			"wardrowbe_set_item_tags/wardrowbe_set_item_description. Only http(s) URLs to public hosts are "+
			"allowed, and an image pasted into the chat cannot be passed here — for a local or pasted "+
			"photo use wardrowbe_create_item_from_base64 instead. Never upload someone's private photos "+
			"to a third-party host to obtain a URL without their explicit consent."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("image_url", mcp.Required(), mcp.Description("Public http(s) URL of the garment image.")),
		mcp.WithString("name", mcp.Description("Optional item name.")),
		mcp.WithString("type", mcp.Description("Optional item type (e.g. shirt, pants, jacket).")),
		mcp.WithString("subtype", mcp.Description("Optional subtype.")),
		mcp.WithString("brand", mcp.Description("Optional brand.")),
		mcp.WithString("notes", mcp.Description("Optional free-text notes/description.")),
		mcp.WithString("primary_color", mcp.Description("Optional primary color.")),
		mcp.WithBoolean("favorite", mcp.Description("Mark as favorite. Default false.")),
		mcp.WithBoolean("auto_tag", mcp.Description("Whether the backend auto-tags the new item. "+
			"Defaults to true. Set false to leave the item pending for external tagging. Only "+
			"meaningful when backend vision is enabled; with vision off every create defers anyway.")),
	), s.handleCreateItemFromURL)

	s.add(mcp.NewTool("wardrowbe_create_item_from_base64",
		mcp.WithDescription("Create a wardrobe item from a base64-encoded image supplied inline. Use this "+
			"when the image lives on the local machine (read the file and pass its bytes as base64) and "+
			"there is no public URL to give wardrowbe_create_item_from_url. The backend auto-tags the uploaded "+
			"image; afterwards refine attributes with wardrowbe_get_item_image + wardrowbe_set_item_tags/"+
			"wardrowbe_set_item_description. Accepts a raw base64 string or a data URL "+
			"(data:image/...;base64,...). Keep images reasonably small — the decoded size is capped at "+
			"20 MiB and large payloads may exceed message limits."),
		mcp.WithDestructiveHintAnnotation(false),
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
		mcp.WithBoolean("auto_tag", mcp.Description("Whether the backend auto-tags the new item. "+
			"Defaults to true. Set false to leave the item pending for external tagging. Only "+
			"meaningful when backend vision is enabled; with vision off every create defers anyway.")),
	), s.handleCreateItemFromBase64)
}

func (s *Server) handleCreateItemFromURL(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	imageURL, err := req.RequireString("image_url")
	if err != nil || imageURL == "" {
		return mcp.NewToolResultError("image_url is required"), nil
	}

	data, mime, filename, err := fetchExternalImage(ctx, imageURL, s.imageHTTPTransport())
	if err != nil {
		return mcp.NewToolResultErrorFromErr("could not fetch image", err), nil
	}

	fields, errRes := itemFields(req)
	if errRes != nil {
		return errRes, nil
	}
	raw, err := s.client.CreateItemFromImage(ctx, data, filename, mime, fields)
	if err != nil {
		return toolErr("create item failed", err), nil
	}
	// Log only the host, not the full URL — retail/CDN URLs often carry signed
	// tokens or tracking params we don't want in production logs.
	s.log.Info("created item from url", "host", hostOnly(imageURL), "bytes", len(data), "mime", mime)
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

	fields, errRes := itemFields(req)
	if errRes != nil {
		return errRes, nil
	}
	result, err := s.client.CreateItemFromImage(ctx, data, filename, mime, fields)
	if err != nil {
		return toolErr("create item failed", err), nil
	}
	s.log.Info("created item from base64", "filename", filename, "bytes", len(data), "mime", mime)
	return jsonText(result), nil
}

// itemFields collects the optional item attributes shared by the create tools.
func itemFields(req mcp.CallToolRequest) (map[string]string, *mcp.CallToolResult) {
	fields := map[string]string{}
	for _, k := range []string{"name", "type", "subtype", "brand", "notes", "primary_color"} {
		if v := req.GetString(k, ""); v != "" {
			fields[k] = v
		}
	}
	if fav, present, errRes := argBool(req, "favorite"); errRes != nil {
		return nil, errRes
	} else if present {
		fields["favorite"] = boolStr(fav)
	}
	if at, present, errRes := argBool(req, "auto_tag"); errRes != nil {
		return nil, errRes
	} else if present {
		fields["auto_tag"] = boolStr(at)
	}
	return fields, nil
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

	// Tolerate the common base64 variants clients emit: standard and URL-safe
	// alphabets, with or without padding.
	data, err := decodeAnyBase64(s)
	if err != nil {
		return nil, "", fmt.Errorf("invalid base64: %w", err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("image is empty")
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("image exceeds %d MiB limit", maxImageBytes>>20)
	}

	// The declared MIME comes straight from caller input and later lands in a
	// multipart header; accept only a strict image/<token> form (no whitespace,
	// control characters or parameters) and otherwise sniff the real type.
	// The token rule is shared with the client layer (wardrowbe.ValidMIMEType),
	// which re-screens whatever reaches the multipart writer.
	mime := declaredMIME
	if !wardrowbe.ValidMIMEType(mime) || !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", fmt.Errorf("decoded bytes are not an image (content-type %q)", mime)
	}
	return data, mime, nil
}

// decodeAnyBase64 decodes standard or URL-safe base64, padded or unpadded.
func decodeAnyBase64(s string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		if data, err := enc.DecodeString(s); err == nil {
			return data, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

// hostOnly returns just the host of a URL for safe logging (no path/query).
func hostOnly(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "unknown"
}

// fetchExternalImage downloads an image from a public URL with an SSRF guard
// (http(s) only, no private/loopback addresses), a size cap, and a content-type
// check. Returns the bytes, sniffed MIME, and a sensible filename. transport is
// shared across calls — building one per fetch would let a keep-alive-holding
// image host pin a connection (fd + goroutines) per call indefinitely.
func fetchExternalImage(ctx context.Context, rawURL string, transport *http.Transport) ([]byte, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", "", fmt.Errorf("only http(s) urls are allowed (got %q)", u.Scheme)
	}

	client := &http.Client{
		Timeout:   imageFetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Re-validate the scheme on every hop: a 3xx can try to bounce us to a
			// non-http(s) scheme, and the initial-URL check above does not cover
			// redirect targets. (The SSRF dialer still re-checks the resolved IP.)
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to non-http(s) scheme %q", req.URL.Scheme)
			}
			if len(via) >= maxImageRedirects {
				return fmt.Errorf("stopped after %d redirects", maxImageRedirects)
			}
			return nil
		},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	httpReq.Header.Set("User-Agent", browserUA)
	httpReq.Header.Set("Accept", "image/*")

	resp, err := client.Do(httpReq)
	if err != nil {
		// The SSRF guard's refusal is a useful, safe signal — surface it. Any
		// other transport error embeds the dialed host/TLS internals, so reduce
		// it to a generic message (mirrors wardrowbe.Client.do).
		if errors.Is(err, errNonPublicAddr) {
			return nil, "", "", errNonPublicAddr
		}
		return nil, "", "", fmt.Errorf("could not reach image host")
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
		// The transport is shared across fetches; bound how long an idle
		// keep-alive connection to an arbitrary external host may linger.
		IdleConnTimeout: 30 * time.Second,
		MaxIdleConns:    8,
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
					return nil, errNonPublicAddr
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
}

// blockedPrefixes are non-public ranges that net.IP's own predicates don't
// cover: IPv4 carrier-grade NAT (RFC 6598, commonly internal) and the NAT64
// well-known prefix (RFC 6052) — NAT64 addresses embed an IPv4 target that a
// NAT64 gateway would translate, which can reach internal IPv4 ranges the
// IPv6 checks don't see. Declared as CIDR strings so each entry is reviewable
// against its RFC.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("64:ff9b::/96"),
}

func isPublicIP(ip net.IP) bool {
	// IsMulticast covers every multicast scope (link-, site-, org-, global- and
	// interface-local), not just the link-local range.
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap() // 4-in-6 form must match the IPv4 prefixes
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return false
		}
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
