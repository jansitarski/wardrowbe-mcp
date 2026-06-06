package wardrowbe

import (
	"bytes"
	"image"
	"image/color"
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
