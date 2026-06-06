package wardrowbe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCoerceList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"bare array", `[{"id":"1"},{"id":"2"}]`, 2},
		{"items key", `{"items":[{"id":"1"}]}`, 1},
		{"results key", `{"results":[1,2,3]}`, 3},
		{"outfits key", `{"outfits":[{"id":"o1"}]}`, 1},
		{"empty array", `[]`, 0},
		{"empty body", ``, 0},
		{"single object wrapped", `{"id":"only"}`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CoerceList(json.RawMessage(tt.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("len = %d, want %d", len(got), tt.want)
			}
		})
	}
}

// fakeProvider returns a fixed dev identity.
type fakeProvider struct{ calls int }

func (f *fakeProvider) SyncPayload(context.Context) (SyncPayload, error) {
	f.calls++
	return SyncPayload{ExternalID: "x", Email: "x@example.com", DisplayName: "X"}, nil
}

func TestRequestResyncsOnceOn401(t *testing.T) {
	var syncCount, itemCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/sync":
			syncCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case "/api/v1/items/abc":
			itemCalls++
			if itemCalls == 1 {
				w.WriteHeader(http.StatusUnauthorized) // first call: force a re-sync
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "abc"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil)
	raw, err := c.Request(context.Background(), http.MethodGet, "/items/abc", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(raw) == "" {
		t.Fatal("expected body after retry")
	}
	if itemCalls != 2 {
		t.Errorf("expected 2 item calls (401 then retry), got %d", itemCalls)
	}
	if syncCount != 2 {
		t.Errorf("expected 2 syncs (initial + forced), got %d", syncCount)
	}
}

func TestRequestReturnsAPIErrorOnPersistent401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil)
	_, err := c.Request(context.Background(), http.MethodGet, "/items/abc", nil, nil)
	if err == nil {
		t.Fatal("expected error on persistent 401")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", apiErr.StatusCode)
	}
}

func TestCreateStudioOutfitPostsExpectedBody(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "outfit-1", "status": "created"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil)
	name := "Friday fit"
	raw, err := c.CreateStudioOutfit(context.Background(), StudioOutfit{
		Items:    []string{"a", "b"},
		Occasion: "casual",
		Name:     &name,
		MarkWorn: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/outfits/studio" {
		t.Errorf("got %s %s, want POST /api/v1/outfits/studio", gotMethod, gotPath)
	}
	if items, _ := gotBody["items"].([]any); len(items) != 2 {
		t.Errorf("items = %v, want 2 entries", gotBody["items"])
	}
	if gotBody["occasion"] != "casual" || gotBody["name"] != "Friday fit" || gotBody["mark_worn"] != true {
		t.Errorf("unexpected body: %#v", gotBody)
	}
	// nil pointers must be omitted — the backend rejects unknown/extra fields.
	if _, ok := gotBody["source_item_id"]; ok {
		t.Error("source_item_id should be omitted when nil")
	}
	if string(raw) == "" {
		t.Error("expected created outfit body")
	}
}

func TestRequest204ReturnsNilBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil)
	raw, err := c.Request(context.Background(), http.MethodPost, "/items/abc/wash", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw != nil {
		t.Errorf("expected nil body for 204, got %q", string(raw))
	}
}
