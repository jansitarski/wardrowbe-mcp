package wardrowbe

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 100, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestDownscaleShrinksLargeImage(t *testing.T) {
	orig := makePNG(t, 1600, 1200)
	out, mime := downscale(orig, "image/png", 768)
	if mime != "image/png" {
		t.Fatalf("mime changed: %s", mime)
	}
	decoded, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode downscaled: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != 768 || b.Dy() != 576 {
		t.Errorf("dims = %dx%d, want 768x576", b.Dx(), b.Dy())
	}
}

func TestDownscaleLeavesSmallImageUntouched(t *testing.T) {
	orig := makePNG(t, 400, 300)
	out, _ := downscale(orig, "image/png", 768)
	if !bytes.Equal(out, orig) {
		t.Error("small image should be returned unchanged")
	}
}

// jpegWithOrientation encodes a w×h JPEG and splices a minimal APP1/Exif
// segment carrying only the given Orientation tag in after SOI.
func jpegWithOrientation(t *testing.T, w, h int, orientation byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 100, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	data := buf.Bytes()
	payload := []byte{
		'E', 'x', 'i', 'f', 0, 0, // Exif header
		'M', 'M', 0, 42, 0, 0, 0, 8, // big-endian TIFF, IFD0 at offset 8
		0, 1, // one entry
		0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orientation, 0, 0, // Orientation (SHORT)
		0, 0, 0, 0, // no next IFD
	}
	app1 := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	out := append([]byte{}, data[:2]...) // SOI
	out = append(out, app1...)
	return append(out, data[2:]...)
}

func TestDownscaleBakesInJPEGOrientation(t *testing.T) {
	orig := jpegWithOrientation(t, 1600, 800, 6) // stored rotated: displays as 800x1600
	if got := jpegOrientation(orig); got != 6 {
		t.Fatalf("fixture orientation = %d, want 6", got)
	}
	out, mime := downscale(orig, "image/jpeg", 768)
	if mime != "image/jpeg" {
		t.Fatalf("mime changed: %s", mime)
	}
	if bytes.Equal(out, orig) {
		t.Fatal("oriented JPEG above maxDim must be downscaled, not passed through at full size")
	}
	decoded, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode downscaled: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != 384 || b.Dy() != 768 {
		t.Errorf("dims = %dx%d, want 384x768 (rotated upright, then scaled)", b.Dx(), b.Dy())
	}
	if got := jpegOrientation(out); got != 1 {
		t.Errorf("re-encoded output still carries orientation %d, want 1 (upright)", got)
	}
}

func TestDownscaleLeavesSmallOrientedJPEGUntouched(t *testing.T) {
	orig := jpegWithOrientation(t, 400, 300, 6)
	out, _ := downscale(orig, "image/jpeg", 768)
	if !bytes.Equal(out, orig) {
		t.Error("small oriented JPEG should keep its original bytes (and EXIF)")
	}
}

func TestDownscaleSkipsUnsupportedFormat(t *testing.T) {
	data := []byte("RIFF....WEBPfake")
	out, mime := downscale(data, "image/webp", 256)
	if mime != "image/webp" || !bytes.Equal(out, data) {
		t.Error("webp should pass through unchanged")
	}
}

func TestResolveImageURL(t *testing.T) {
	c := &Client{baseURL: "http://backend:8000"}
	keys := variantFields["medium"]

	t.Run("absolute signed url is unauthenticated", func(t *testing.T) {
		got, authed, err := c.resolveImageURL(
			map[string]any{"medium_url": "https://cdn.example/x.jpg?sig=abc"}, keys)
		if err != nil {
			t.Fatal(err)
		}
		if authed {
			t.Error("absolute url should not attach bearer")
		}
		if got != "https://cdn.example/x.jpg?sig=abc" {
			t.Errorf("url = %s", got)
		}
	})

	t.Run("relative url prefixes base and authenticates", func(t *testing.T) {
		got, authed, err := c.resolveImageURL(
			map[string]any{"medium_url": "/api/v1/images/u/x.jpg"}, keys)
		if err != nil || !authed {
			t.Fatalf("authed=%v err=%v", authed, err)
		}
		if got != "http://backend:8000/api/v1/images/u/x.jpg" {
			t.Errorf("url = %s", got)
		}
	})

	t.Run("path falls back to images endpoint", func(t *testing.T) {
		got, authed, err := c.resolveImageURL(
			map[string]any{"medium_path": "/data/uploads/u123/photo.jpg", "user_id": "u123"}, keys)
		if err != nil || !authed {
			t.Fatalf("authed=%v err=%v", authed, err)
		}
		want := "http://backend:8000/api/v1/images/u123/photo.jpg"
		if got != want {
			t.Errorf("url = %s, want %s", got, want)
		}
	})

	t.Run("missing everything errors", func(t *testing.T) {
		if _, _, err := c.resolveImageURL(map[string]any{}, keys); err == nil {
			t.Error("expected error when no url or path present")
		}
	})
}

func TestResolveBackendImageURL(t *testing.T) {
	c := &Client{baseURL: "https://wardrowbe.example.com"}

	t.Run("accepts", func(t *testing.T) {
		cases := map[string]string{
			"relative signed path": "/api/v1/images/u/x.jpg?expires=1&sig=abc",
			"same-host full url":   "https://wardrowbe.example.com/api/v1/images/u/x.jpg?sig=abc",
		}
		wants := map[string]string{
			"relative signed path": "https://wardrowbe.example.com/api/v1/images/u/x.jpg?expires=1&sig=abc",
			"same-host full url":   "https://wardrowbe.example.com/api/v1/images/u/x.jpg?sig=abc",
		}
		for name, ref := range cases {
			t.Run(name, func(t *testing.T) {
				got, err := c.resolveBackendImageURL(ref)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != wants[name] {
					t.Errorf("url = %s, want %s", got, wants[name])
				}
			})
		}
	})

	t.Run("rejects", func(t *testing.T) {
		cases := map[string]string{
			"empty":                "",
			"other host":           "https://evil.example.com/api/v1/images/u/x.jpg",
			"non-http scheme":      "file:///etc/passwd",
			"protocol-relative":    "//evil.example.com/api/v1/images/u/x.jpg",
			"relative non-image":   "/api/v1/items/123",
			"same-host non-image":  "https://wardrowbe.example.com/api/v1/items/123",
			"not absolute path":    "api/v1/images/u/x.jpg",
			"path traversal scope": "/api/v1/../secrets",
		}
		for name, ref := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := c.resolveBackendImageURL(ref); err == nil {
					t.Errorf("expected rejection for %q", ref)
				}
			})
		}
	})
}
