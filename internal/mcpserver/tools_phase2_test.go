package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

// capturedRequest records the last non-auth request a recording backend saw, so
// the Phase 2 tests can assert on the method, path, query and decoded body the
// MCP handler actually drove.
type capturedRequest struct {
	method string
	path   string
	query  url.Values
	body   []byte
}

// newRecordingServer builds a Server whose client points at an httptest backend
// that answers /auth/sync and records every other request into cap. The backend
// replies to the recorded request with reply (status 200).
func newRecordingServer(t *testing.T, cap *capturedRequest, reply any) *Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		body, _ := io.ReadAll(r.Body)
		cap.method, cap.path, cap.query, cap.body = r.Method, r.URL.Path, r.URL.Query(), body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)

	wc := wardrowbe.NewClient(srv.URL, wardrowbe.DevTokenProvider{ExternalID: "t"},
		&http.Client{Timeout: 5 * time.Second}, slog.Default())
	return &Server{client: wc, log: slog.Default()}
}

// toolReq builds a CallToolRequest carrying the given arguments.
func toolReq(args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	return req
}

// Gap 1 — list_items forwards tagging_status when set, omits it otherwise.
func TestListItemsForwardsTaggingStatus(t *testing.T) {
	t.Run("pending forwarded", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"items": []any{}})
		res, _ := s.handleListItems(context.Background(), toolReq(map[string]any{"tagging_status": "pending"}))
		if res.IsError {
			t.Fatalf("unexpected error: %s", firstErrText(res))
		}
		if got := cap.query.Get("tagging_status"); got != "pending" {
			t.Errorf("tagging_status query = %q, want pending", got)
		}
	})

	t.Run("omitted means no key", func(t *testing.T) {
		var cap capturedRequest
		s := newRecordingServer(t, &cap, map[string]any{"items": []any{}})
		if _, err := s.handleListItems(context.Background(), toolReq(map[string]any{})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := cap.query["tagging_status"]; ok {
			t.Errorf("tagging_status should be absent, got %q", cap.query.Get("tagging_status"))
		}
	})
}

// Gap 1 — the convenience tool hard-sets tagging_status=pending.
func TestListUntaggedItemsHardSetsPending(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"items": []any{}})
	if _, err := s.handleListUntaggedItems(context.Background(), toolReq(map[string]any{"limit": 5})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cap.query.Get("tagging_status"); got != "pending" {
		t.Errorf("tagging_status = %q, want pending", got)
	}
	if got := cap.query.Get("page_size"); got != "5" {
		t.Errorf("page_size = %q, want 5", got)
	}
}

// set_item_tags sends a single tags payload; the backend projects tag attributes
// onto their first-class columns on write-back, so nothing is sent top-level.
func TestSetItemTagsSendsSingleTagsPayload(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"id": "item-1"})
	_, err := s.handleSetItemTags(context.Background(), toolReq(map[string]any{
		"item_id":       "item-1",
		"colors":        []any{"navy", "white"},
		"primary_color": "navy",
		"pattern":       "striped",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(cap.body, &raw); err != nil {
		t.Fatalf("decode body %q: %v", cap.body, err)
	}
	tags, _ := raw["tags"].(map[string]any)
	if tags == nil {
		t.Fatalf("tags payload missing: %s", cap.body)
	}
	if tags["primary_color"] != "navy" || tags["pattern"] != "striped" {
		t.Errorf("tags not populated: %v", tags)
	}
	if colors, _ := tags["colors"].([]any); len(colors) != 2 {
		t.Errorf("tags.colors = %v, want 2 entries", tags["colors"])
	}
	for _, col := range []string{"colors", "primary_color", "pattern"} {
		if _, ok := raw[col]; ok {
			t.Errorf("%s must not be sent top-level; the backend projects columns from tags", col)
		}
	}
}

// Gap 3a — auto_tag=false is forwarded as a multipart form field on create.
func TestCreateForwardsAutoTag(t *testing.T) {
	var cap capturedRequest
	var formValue string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
			return
		}
		_ = r.ParseMultipartForm(8 << 20)
		formValue = r.FormValue("auto_tag")
		cap.method, cap.path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "item-new", "tagging_status": "pending"})
	}))
	t.Cleanup(srv.Close)

	wc := wardrowbe.NewClient(srv.URL, wardrowbe.DevTokenProvider{ExternalID: "t"},
		&http.Client{Timeout: 5 * time.Second}, slog.Default())
	s := &Server{client: wc, log: slog.Default()}

	res, err := s.handleCreateItemFromBase64(context.Background(), toolReq(map[string]any{
		"image_base64": "data:image/png;base64," + pngB64(),
		"auto_tag":     false,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", firstErrText(res))
	}
	if formValue != "false" {
		t.Errorf("auto_tag form field = %q, want false", formValue)
	}
}

// Gap 3b — retag_item POSTs /items/{id}/retag.
func TestRetagItemPostsRetag(t *testing.T) {
	var cap capturedRequest
	s := newRecordingServer(t, &cap, map[string]any{"ok": true})
	if _, err := s.handleRetagItem(context.Background(), toolReq(map[string]any{"item_id": "item-1"})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/api/v1/items/item-1/retag" {
		t.Errorf("got %s %s, want POST /api/v1/items/item-1/retag", cap.method, cap.path)
	}
}

// firstErrText extracts the text of a tool result (used for error assertions).
func firstErrText(res *mcp.CallToolResult) string {
	for _, ct := range res.Content {
		if tc, ok := ct.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// pngB64 returns a base64 PNG payload the create handler accepts as an image.
func pngB64() string {
	return "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
}
