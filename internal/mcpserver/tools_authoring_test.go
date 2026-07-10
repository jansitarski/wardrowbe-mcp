package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
)

// create_outfit_suggestion drives POST /outfits/suggestions with the full
// authored payload; item order is preserved and optionals are omitted when unset.
func TestCreateSuggestionSendsAuthoredPayload(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"id": "outfit-1"})

	res, err := s.handleCreateSuggestion(context.Background(), toolReq(map[string]any{
		"item_ids":    []any{"i-2", "i-1", "i-3"},
		"occasion":    "casual",
		"reasoning":   "light layers",
		"style_notes": "roll the sleeves",
		"season":      "summer",
		"formality":   "casual",
		"palette":     []any{"navy", "white"},
		"notes":       "for the lake trip",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", firstErrText(res))
	}
	if cap.method != "POST" || cap.path != "/api/v1/outfits/suggestions" {
		t.Errorf("request = %s %s, want POST /api/v1/outfits/suggestions", cap.method, cap.path)
	}
	var raw map[string]any
	if err := json.Unmarshal(cap.body, &raw); err != nil {
		t.Fatalf("decode body %q: %v", cap.body, err)
	}
	items, _ := raw["items"].([]any)
	if len(items) != 3 || items[0] != "i-2" || items[2] != "i-3" {
		t.Errorf("items = %v, want request order [i-2 i-1 i-3]", raw["items"])
	}
	for key, want := range map[string]string{
		"occasion": "casual", "reasoning": "light layers", "style_notes": "roll the sleeves",
		"season": "summer", "formality": "casual", "notes": "for the lake trip",
	} {
		if raw[key] != want {
			t.Errorf("%s = %v, want %q", key, raw[key], want)
		}
	}
	if palette, _ := raw["palette"].([]any); len(palette) != 2 {
		t.Errorf("palette = %v, want 2 entries", raw["palette"])
	}
}

// Unset optionals must be omitted from the wire payload, not sent as nulls —
// the backend request models reject unknown fields but treat null and absent
// alike; omitting keeps payloads minimal and mirrors set_item_tags behavior.
func TestCreateSuggestionOmitsUnsetOptionals(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"id": "outfit-1"})

	if _, err := s.handleCreateSuggestion(context.Background(), toolReq(map[string]any{
		"item_ids": []any{"i-1"},
		"occasion": "office",
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(cap.body, &raw); err != nil {
		t.Fatalf("decode body %q: %v", cap.body, err)
	}
	for _, key := range []string{"name", "scheduled_for", "reasoning", "style_notes", "season", "formality", "palette", "notes"} {
		if _, ok := raw[key]; ok {
			t.Errorf("%s should be omitted when unset, got %v", key, raw[key])
		}
	}
}

func TestCreateSuggestionValidation(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing item_ids", map[string]any{"occasion": "casual"}},
		{"missing occasion", map[string]any{"item_ids": []any{"i-1"}}},
		{"invalid occasion", map[string]any{"item_ids": []any{"i-1"}, "occasion": "space-walk"}},
		{"empty item id", map[string]any{"item_ids": []any{" "}, "occasion": "casual"}},
		{"invalid season", map[string]any{"item_ids": []any{"i-1"}, "occasion": "casual", "season": "monsoon"}},
		{"invalid formality", map[string]any{"item_ids": []any{"i-1"}, "occasion": "casual", "formality": "black-tie"}},
		{"empty palette color", map[string]any{"item_ids": []any{"i-1"}, "occasion": "casual", "palette": []any{" "}}},
		{"bad date", map[string]any{"item_ids": []any{"i-1"}, "occasion": "casual", "scheduled_for": "2026-6-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cap capturedRequest
			s := newRecordingServer(t, &cap, map[string]any{})
			res, err := s.handleCreateSuggestion(context.Background(), toolReq(tc.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.IsError {
				t.Fatal("want tool error")
			}
			if cap.method != "" {
				t.Errorf("backend should not be called, saw %s %s", cap.method, cap.path)
			}
		})
	}
}

// create_item_pairing drives POST /pairings/item/{id} with the partner list.
func TestCreatePairingSendsAuthoredPayload(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"id": "pairing-1"})

	res, err := s.handleCreatePairing(context.Background(), toolReq(map[string]any{
		"item_id":   "src-1",
		"item_ids":  []any{"i-1", "i-2"},
		"reasoning": "denim anchors it",
		"season":    "fall",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", firstErrText(res))
	}
	if cap.method != "POST" || cap.path != "/api/v1/pairings/item/src-1" {
		t.Errorf("request = %s %s, want POST /api/v1/pairings/item/src-1", cap.method, cap.path)
	}
	var raw map[string]any
	if err := json.Unmarshal(cap.body, &raw); err != nil {
		t.Fatalf("decode body %q: %v", cap.body, err)
	}
	if items, _ := raw["items"].([]any); len(items) != 2 {
		t.Errorf("items = %v, want 2 partners", raw["items"])
	}
	if raw["reasoning"] != "denim anchors it" || raw["season"] != "fall" {
		t.Errorf("payload = %v", raw)
	}
}

func TestCreatePairingValidation(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing item_id", map[string]any{"item_ids": []any{"i-1"}}},
		{"missing partners", map[string]any{"item_id": "src-1"}},
		{"empty partner id", map[string]any{"item_id": "src-1", "item_ids": []any{""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cap capturedRequest
			s := newRecordingServer(t, &cap, map[string]any{})
			res, err := s.handleCreatePairing(context.Background(), toolReq(tc.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.IsError {
				t.Fatal("want tool error")
			}
			if cap.method != "" {
				t.Errorf("backend should not be called, saw %s %s", cap.method, cap.path)
			}
		})
	}
}

// create_outfit (studio) forwards the shared authoring attributes when given
// and keeps omitting them otherwise.
func TestCreateOutfitForwardsAuthoringAttributes(t *testing.T) {
	t.Run("forwarded when set", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"id": "outfit-1"})
		if _, err := s.handleCreateOutfit(context.Background(), toolReq(map[string]any{
			"item_ids":  []any{"i-1"},
			"occasion":  "casual",
			"season":    "spring",
			"formality": "smart-casual",
			"palette":   []any{"green"},
			"notes":     "studio with attributes",
		})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(cap.body, &raw); err != nil {
			t.Fatalf("decode body %q: %v", cap.body, err)
		}
		if raw["season"] != "spring" || raw["formality"] != "smart-casual" || raw["notes"] != "studio with attributes" {
			t.Errorf("attributes not forwarded: %v", raw)
		}
	})

	t.Run("omitted when unset", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"id": "outfit-1"})
		if _, err := s.handleCreateOutfit(context.Background(), toolReq(map[string]any{
			"item_ids": []any{"i-1"},
			"occasion": "casual",
		})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(cap.body, &raw); err != nil {
			t.Fatalf("decode body %q: %v", cap.body, err)
		}
		for _, key := range []string{"season", "formality", "palette", "notes"} {
			if _, ok := raw[key]; ok {
				t.Errorf("%s should be omitted when unset, got %v", key, raw[key])
			}
		}
	})
}
