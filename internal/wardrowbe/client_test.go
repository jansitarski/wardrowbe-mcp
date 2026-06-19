package wardrowbe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCoerceListTreatsNullAsEmpty(t *testing.T) {
	for _, in := range []string{"null", "  null ", ""} {
		got, err := CoerceList([]byte(in))
		if err != nil {
			t.Errorf("CoerceList(%q) error: %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("CoerceList(%q) = %d items, want 0", in, len(got))
		}
	}
}

func TestAPIErrorDoesNotLeakBody(t *testing.T) {
	err := &APIError{
		StatusCode: 400,
		Method:     http.MethodGet,
		Path:       "/items/x",
		Body:       `{"detail":"token eyJsecret leaked"}`,
	}
	msg := err.Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "/items/x") {
		t.Errorf("error should report status and path, got %q", msg)
	}
	if strings.Contains(msg, "eyJsecret") || strings.Contains(msg, "detail") {
		t.Errorf("error must NOT surface the backend body, got %q", msg)
	}
}

func TestReadBoundedBodyRejectsOversize(t *testing.T) {
	if _, err := readBoundedBody(bytes.NewReader(make([]byte, maxAPIResponseBytes+1))); err == nil {
		t.Error("expected error for oversize body")
	}
	if data, err := readBoundedBody(bytes.NewReader([]byte("ok"))); err != nil || string(data) != "ok" {
		t.Errorf("small body: got %q, err %v", data, err)
	}
}

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

// oidcFakeProvider returns an identity carrying a raw id_token, mirroring the
// OIDC provider so we can assert the token reaches the sync body.
type oidcFakeProvider struct{}

func (oidcFakeProvider) SyncPayload(context.Context) (SyncPayload, error) {
	return SyncPayload{ExternalID: "user-123", Email: "u@example.com", DisplayName: "U", IDToken: "header.payload.sig"}, nil
}

func TestSyncForwardsIDTokenInBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/sync":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case "/api/v1/items/abc":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "abc"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, oidcFakeProvider{}, srv.Client(), nil)
	if _, err := c.Request(context.Background(), http.MethodGet, "/items/abc", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["id_token"] != "header.payload.sig" {
		t.Errorf("sync body id_token = %v, want the raw token forwarded to the backend", gotBody["id_token"])
	}
	if gotBody["external_id"] != "user-123" {
		t.Errorf("sync body external_id = %v, want user-123", gotBody["external_id"])
	}
}

func TestSyncSendsAgentKeyHeaderWhenConfigured(t *testing.T) {
	var gotHeader string
	var headerPresent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/sync":
			gotHeader = r.Header.Get("X-Wardrowbe-Agent-Key")
			_, headerPresent = r.Header["X-Wardrowbe-Agent-Key"]
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case "/api/v1/items/abc":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "abc"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil, WithAgentSyncKey("s3cr3t"))
	if _, err := c.Request(context.Background(), http.MethodGet, "/items/abc", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "s3cr3t" {
		t.Errorf("X-Wardrowbe-Agent-Key = %q, want s3cr3t", gotHeader)
	}
	// The secret must ride the header, never the request body.
	if !headerPresent {
		t.Error("X-Wardrowbe-Agent-Key header should be present when configured")
	}
}

func TestSyncOmitsAgentKeyHeaderByDefault(t *testing.T) {
	var headerPresent bool
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/sync":
			_, headerPresent = r.Header["X-Wardrowbe-Agent-Key"]
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case "/api/v1/items/abc":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "abc"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil)
	if _, err := c.Request(context.Background(), http.MethodGet, "/items/abc", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if headerPresent {
		t.Error("X-Wardrowbe-Agent-Key header must be absent when not configured")
	}
	// And it must never leak into the body under any key.
	if _, ok := gotBody["actor"]; ok {
		t.Error("sync body must not carry an actor field (contract is header-only)")
	}
	if _, ok := gotBody["agent_key"]; ok {
		t.Error("sync body must not carry the agent key")
	}
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

// makeJWT builds an unsigned-payload JWT carrying the given exp (the client
// reads exp without verifying the signature).
func makeJWT(exp int64) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." +
		enc(map[string]int64{"exp": exp}) + ".sig"
}

func TestCachesTokenWhenExpiresInNull(t *testing.T) {
	// Reproduces the rate-limit bug: dev /auth/sync omits expires_in but the JWT
	// carries a future exp. The client must cache it and NOT re-sync per request.
	var syncCount int
	jwt := makeJWT(time.Now().Add(time.Hour).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/sync" {
			syncCount++
			// expires_in deliberately absent (null) — mirrors the dev backend.
			_, _ = w.Write([]byte(`{"access_token":"` + jwt + `"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil)
	for i := 0; i < 5; i++ {
		if _, err := c.Request(context.Background(), http.MethodGet, "/items", nil, nil); err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}
	if syncCount != 1 {
		t.Errorf("expected exactly 1 auth sync across 5 requests, got %d (token not cached)", syncCount)
	}
}

func TestTokenTTLFallbacks(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).Unix()
	tests := []struct {
		name string
		sr   syncResponse
		min  time.Duration // expected ttl is at least this
	}{
		{"explicit expires_in", syncResponse{AccessToken: "x", ExpiresIn: 3600}, time.Hour - refreshLeeway - time.Second},
		{"null expires_in uses jwt exp", syncResponse{AccessToken: makeJWT(future)}, 90 * time.Minute},
		{"no exp, no expires_in uses default", syncResponse{AccessToken: "not-a-jwt"}, defaultTokenTTL - refreshLeeway - time.Second},
		{"expired jwt clamps to min", syncResponse{AccessToken: makeJWT(time.Now().Add(-time.Hour).Unix())}, minTokenTTL - time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenTTL(tt.sr)
			if got < tt.min {
				t.Errorf("ttl = %v, want >= %v", got, tt.min)
			}
			if got < minTokenTTL {
				t.Errorf("ttl = %v below floor %v", got, minTokenTTL)
			}
		})
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

func TestDeleteOutfitIssuesDeleteAndHandles204(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, &fakeProvider{}, srv.Client(), nil)
	raw, err := c.DeleteOutfit(context.Background(), "o-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/outfits/o-123" {
		t.Errorf("got %s %s, want DELETE /api/v1/outfits/o-123", gotMethod, gotPath)
	}
	if raw != nil {
		t.Errorf("expected nil body for 204, got %q", string(raw))
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
