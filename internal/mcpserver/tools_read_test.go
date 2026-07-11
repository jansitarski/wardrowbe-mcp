package mcpserver

import (
	"context"
	"testing"
)

// get_recent_outfits paginates with page_size (the backend ignores a bare
// "limit" param) and forwards each filter only when set.
func TestGetRecentOutfitsSendsPageSizeAndFilters(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"outfits": []any{}})

	res, err := s.handleRecentOutfits(context.Background(), toolReq(map[string]any{
		"limit":       5,
		"status":      "pending",
		"source":      "external",
		"occasion":    "casual",
		"date_from":   "2026-07-01",
		"date_to":     "2026-07-31",
		"is_lookbook": false,
		"search":      "summer",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", firstErrText(res))
	}
	if cap.path != "/api/v1/outfits" {
		t.Errorf("path = %s, want /api/v1/outfits", cap.path)
	}
	if _, ok := cap.query["limit"]; ok {
		t.Error("limit must not be sent; the backend paginates with page_size")
	}
	for key, want := range map[string]string{
		"page_size": "5", "status": "pending", "source": "external", "occasion": "casual",
		"date_from": "2026-07-01", "date_to": "2026-07-31", "is_lookbook": "false", "search": "summer",
	} {
		if got := cap.query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestGetRecentOutfitsOmitsUnsetFilters(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"outfits": []any{}})

	if _, err := s.handleRecentOutfits(context.Background(), toolReq(map[string]any{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cap.query.Get("page_size"); got != "10" {
		t.Errorf("page_size = %q, want default 10", got)
	}
	for _, key := range []string{"status", "source", "occasion", "date_from", "date_to", "is_lookbook", "search"} {
		if _, ok := cap.query[key]; ok {
			t.Errorf("%s should be absent when unset, got %q", key, cap.query.Get(key))
		}
	}
}

func TestGetRecentOutfitsRejectsInvalidFilters(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"invalid source", map[string]any{"source": "wormhole"}},
		{"invalid occasion", map[string]any{"occasion": "space-walk"}},
		{"bad date", map[string]any{"date_from": "2026-7-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cap capturedRequest
			s := newRecordingServer(t, &cap, map[string]any{})
			res, err := s.handleRecentOutfits(context.Background(), toolReq(tc.args))
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

// list_pairings hits the collection by default and the per-item route when
// item_id is set (where source_type does not apply).
func TestListPairingsRoutes(t *testing.T) {
	t.Run("collection with source_type", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"pairings": []any{}})
		if _, err := s.handleListPairings(context.Background(), toolReq(map[string]any{
			"source_type": "shirt", "page_size": 50,
		})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cap.path != "/api/v1/pairings" {
			t.Errorf("path = %s, want /api/v1/pairings", cap.path)
		}
		if got := cap.query.Get("source_type"); got != "shirt" {
			t.Errorf("source_type = %q, want shirt", got)
		}
		if got := cap.query.Get("page_size"); got != "50" {
			t.Errorf("page_size = %q, want 50", got)
		}
	})

	t.Run("per-item route drops source_type", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"pairings": []any{}})
		if _, err := s.handleListPairings(context.Background(), toolReq(map[string]any{
			"item_id": "src-1", "source_type": "shirt",
		})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cap.path != "/api/v1/pairings/item/src-1" {
			t.Errorf("path = %s, want /api/v1/pairings/item/src-1", cap.path)
		}
		if _, ok := cap.query["source_type"]; ok {
			t.Error("source_type should not be sent on the per-item route")
		}
	})
}

// The weather handlers forward coordinate overrides and the forecast's days.
func TestWeatherForwardsParams(t *testing.T) {
	t.Run("current with overrides", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"temperature": 20})
		handler := s.handleWeather("/weather/current", false)
		if _, err := handler(context.Background(), toolReq(map[string]any{
			"latitude": 54.35, "longitude": 18.65,
		})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cap.path != "/api/v1/weather/current" {
			t.Errorf("path = %s, want /api/v1/weather/current", cap.path)
		}
		if got := cap.query.Get("latitude"); got != "54.35" {
			t.Errorf("latitude = %q, want 54.35", got)
		}
		if _, ok := cap.query["days"]; ok {
			t.Error("days should not be sent to /weather/current")
		}
	})

	t.Run("forecast defaults days and omits absent coords", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"days": []any{}})
		handler := s.handleWeather("/weather/forecast", true)
		if _, err := handler(context.Background(), toolReq(map[string]any{})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := cap.query.Get("days"); got != "7" {
			t.Errorf("days = %q, want default 7", got)
		}
		for _, key := range []string{"latitude", "longitude"} {
			if _, ok := cap.query[key]; ok {
				t.Errorf("%s should be absent when unset", key)
			}
		}
	})

	t.Run("rejects non-numeric coordinates", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{})
		handler := s.handleWeather("/weather/current", false)
		res, err := handler(context.Background(), toolReq(map[string]any{"latitude": "north"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsError {
			t.Fatal("want tool error for non-numeric latitude")
		}
	})
}

func TestItemPairSuggestionsForwardsLimit(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, []any{})
	if _, err := s.handleItemPairSuggestions(context.Background(), toolReq(map[string]any{
		"item_id": "item-1", "limit": 3,
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.path != "/api/v1/learning/item-pairs/item-1" {
		t.Errorf("path = %s, want /api/v1/learning/item-pairs/item-1", cap.path)
	}
	if got := cap.query.Get("limit"); got != "3" {
		t.Errorf("limit = %q, want 3", got)
	}
}

// The fixed-path read tools hit their documented endpoints.
func TestSimpleReadToolPaths(t *testing.T) {
	for path, name := range map[string]string{
		"/api/v1/capabilities":         "capabilities",
		"/api/v1/users/me/preferences": "preferences",
		"/api/v1/learning":             "learning insights",
	} {
		t.Run(name, func(t *testing.T) {
			var cap capturedRequest
			s := newRecordingServer(t, &cap, map[string]any{})
			backendPath := path[len("/api/v1"):]
			if _, err := s.simpleGet(backendPath)(context.Background(), toolReq(nil)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cap.path != path {
				t.Errorf("path = %s, want %s", cap.path, path)
			}
		})
	}
}
