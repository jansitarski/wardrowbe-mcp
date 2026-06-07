package mcpserver

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// pngBytes is a minimal byte slice whose PNG signature is enough for
// http.DetectContentType to classify it as image/png.
var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}

func TestIsPublicIP(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":          true,
		"1.1.1.1":          true,
		"127.0.0.1":        false, // loopback
		"10.0.0.5":         false, // private
		"192.168.1.10":     false, // private
		"172.16.0.1":       false, // private
		"169.254.1.1":      false, // link-local
		"100.64.0.1":       false, // CGNAT
		"0.0.0.0":          false, // unspecified
		"::1":              false, // ipv6 loopback
		"fd00::1":          false, // ipv6 ULA (private)
		"2606:4700:4700::": true,  // public ipv6
	}
	for ipStr, want := range cases {
		t.Run(ipStr, func(t *testing.T) {
			if got := isPublicIP(net.ParseIP(ipStr)); got != want {
				t.Errorf("isPublicIP(%s) = %v, want %v", ipStr, got, want)
			}
		})
	}
}

func TestFetchExternalImageRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://host/x.jpg", "data:image/png;base64,xx"} {
		if _, _, _, err := fetchExternalImage(context.Background(), u); err == nil {
			t.Errorf("expected rejection for %q", u)
		}
	}
}

func TestFetchExternalImageRejectsLoopbackOrNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer srv.Close()
	// httptest listens on 127.0.0.1 — the SSRF guard refuses the dial, which is
	// itself a correct failure (and also exercises that path).
	if _, _, _, err := fetchExternalImage(context.Background(), srv.URL); err == nil {
		t.Error("expected error fetching a loopback / non-image host")
	}
}

func TestDecodeBase64ImageRawAndDataURL(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	cases := map[string]string{
		"raw":              b64,
		"data url":         "data:image/png;base64," + b64,
		"data url no mime": "data:;base64," + b64,
		// Some clients wrap base64 in newlines — the decoder must tolerate it.
		"whitespace": "  " + b64[:4] + "\n" + b64[4:] + "  ",
		// Unpadded standard and URL-safe alphabets must also decode.
		"raw std unpadded": strings.TrimRight(b64, "="),
		"url safe":         base64.URLEncoding.EncodeToString(pngBytes),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			data, mime, err := decodeBase64Image(in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mime != "image/png" {
				t.Errorf("mime = %q, want image/png", mime)
			}
			if len(data) != len(pngBytes) {
				t.Errorf("len(data) = %d, want %d", len(data), len(pngBytes))
			}
		})
	}
}

func TestDecodeBase64ImageRejectsBadInput(t *testing.T) {
	notImage := base64.StdEncoding.EncodeToString([]byte("just some plain text, definitely not an image"))
	cases := map[string]string{
		"invalid base64":      "!!!not base64!!!",
		"empty":               "",
		"non-image bytes":     notImage,
		"data url not base64": "data:image/png,raw",
		"data url no comma":   "data:image/png;base64",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeBase64Image(in); err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}

func TestDecodeBase64ImageRejectsOversize(t *testing.T) {
	big := make([]byte, 0, maxImageBytes+16)
	big = append(big, pngBytes...)
	big = append(big, make([]byte, maxImageBytes+1)...)
	if _, _, err := decodeBase64Image(base64.StdEncoding.EncodeToString(big)); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Errorf("expected size-limit error, got %v", err)
	}
}

func TestFilenameFor(t *testing.T) {
	cases := []struct{ raw, mime, want string }{
		{"https://cdn.x/a/b/photo.jpg", "image/jpeg", "photo.jpg"},
		{"https://cdn.x/img", "image/png", "item.png"},
		{"https://cdn.x/", "image/webp", "item.webp"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := filenameFor(u, c.mime); got != c.want {
			t.Errorf("filenameFor(%s,%s) = %s, want %s", c.raw, c.mime, got, c.want)
		}
	}
}
