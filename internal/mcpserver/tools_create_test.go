package mcpserver

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

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
