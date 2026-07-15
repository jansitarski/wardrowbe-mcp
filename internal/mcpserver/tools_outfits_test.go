package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactOutfitListProjectsWhitelistedFields(t *testing.T) {
	raw := json.RawMessage(`{
		"outfits": [{
			"id": "o-1", "occasion": "casual", "status": "pending",
			"name": "Friday Fit", "scheduled_for": "2026-07-17",
			"created_at": "2026-07-15T10:00:00Z",
			"reasoning": "long model rambling",
			"weather": {"temp": 21},
			"items": [{
				"id": "i-1", "type": "shirt", "name": "Blue Shirt",
				"image_url": "https://x/signed?sig=abc",
				"thumbnail_url": "https://x/signed-thumb?sig=def",
				"primary_color": "blue", "position": 0
			}]
		}],
		"total": 1, "page": 1, "page_size": 10, "has_more": false
	}`)

	res, err := compactOutfitList(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", firstErrText(res))
	}
	text := firstErrText(res)

	for _, want := range []string{"o-1", "Friday Fit", "pending", "casual",
		"2026-07-17", "2026-07-15T10:00:00Z", "i-1", "shirt", "Blue Shirt",
		`"total": 1`, `"has_more": false`} {
		if !strings.Contains(text, want) {
			t.Errorf("compact output missing %q:\n%s", want, text)
		}
	}
	for _, dropped := range []string{"image_url", "thumbnail_url", "signed",
		"reasoning", "weather", "primary_color", "page_size"} {
		if strings.Contains(text, dropped) {
			t.Errorf("compact output should drop %q:\n%s", dropped, text)
		}
	}
}

func TestCompactOutfitListEmptyList(t *testing.T) {
	res, err := compactOutfitList(json.RawMessage(`{"outfits": [], "total": 0}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(firstErrText(res), `"outfits": []`) {
		t.Errorf("empty list should marshal as [], got:\n%s", firstErrText(res))
	}
}

func TestCompactOutfitListMalformedBackendJSON(t *testing.T) {
	res, err := compactOutfitList(json.RawMessage(`{"outfits": "not-a-list"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("malformed backend payload should produce a tool error")
	}
}
