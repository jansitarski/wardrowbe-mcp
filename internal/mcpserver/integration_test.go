//go:build integration

// Package mcpserver integration test: drives every registered tool through the
// real MCP protocol (in-process transport) against a faithful in-memory stand-in
// for the Wardrowbe backend, asserting each tool's happy path returns a non-error
// result and that the security/validation guards reject bad input.
//
// Hermetic: no subprocess, no ports beyond an httptest loopback server, no
// outbound network. Run with: go test -tags integration ./internal/mcpserver/
package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/config"
	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// tinyPNG is a 1x1 PNG returned for every image variant.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

// expectedToolCount must track the number of tools registered by registerTools.
const expectedToolCount = 31

func mockBackend(t *testing.T) *httptest.Server {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}

	item := func(id, name string) map[string]any {
		return map[string]any{
			"id": id, "name": name, "type": "shirt", "user_id": "u-1",
			"primary_color": "blue", "wear_count": 5, "needs_wash": false,
			"thumbnail_url": "/media/" + id + "-thumb.png",
			"medium_url":    "/media/" + id + "-medium.png",
			"image_url":     "/media/" + id + "-full.png",
		}
	}
	outfit := func(id string) map[string]any {
		return map[string]any{
			"id": id, "occasion": "casual", "status": "pending",
			"items": []any{item("item-1", "Blue Shirt"), item("item-2", "Black Jeans")},
		}
	}
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		seg := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/"), "/")
		switch seg[0] {
		case "health":
			writeJSON(w, 200, map[string]any{"status": "ok"})
		case "auth":
			switch seg[1] {
			case "sync":
				writeJSON(w, 200, map[string]any{"access_token": "mock-token", "expires_in": 3600})
			case "config":
				writeJSON(w, 200, map[string]any{"auth_mode": "dev", "providers": []string{}})
			case "session":
				writeJSON(w, 200, map[string]any{"user_id": "u-1", "external_id": "test-user", "email": "t@example.com"})
			default:
				http.NotFound(w, r)
			}
		case "analytics":
			writeJSON(w, 200, map[string]any{
				"total_items": 2,
				"most_worn_items": []any{
					map[string]any{"id": "item-1", "name": "Blue Shirt", "wear_count": 12},
					map[string]any{"id": "item-2", "name": "Black Jeans", "wear_count": 7},
				},
			})
		case "notifications":
			if len(seg) >= 2 && seg[1] == "history" {
				writeJSON(w, 200, map[string]any{"notifications": []any{
					map[string]any{"id": "n-1", "type": "wash_reminder", "message": "Wash time"},
				}})
				return
			}
			if len(seg) >= 4 && seg[1] == "settings" && seg[3] == "test" {
				writeJSON(w, 200, map[string]any{"ok": true, "sent": true, "setting_id": seg[2]})
				return
			}
			http.NotFound(w, r)
		case "items":
			switch len(seg) {
			case 1:
				if r.Method == http.MethodPost {
					_ = r.ParseMultipartForm(32 << 20)
					writeJSON(w, 201, map[string]any{"id": "item-new", "status": "processing", "name": r.FormValue("name")})
					return
				}
				writeJSON(w, 200, map[string]any{"items": []any{item("item-1", "Blue Shirt"), item("item-2", "Black Jeans")}})
			case 2:
				if r.Method == http.MethodPatch {
					var patch map[string]any
					_ = json.NewDecoder(r.Body).Decode(&patch)
					it := item(seg[1], "Blue Shirt")
					for k, v := range patch {
						it[k] = v
					}
					writeJSON(w, 200, it)
					return
				}
				writeJSON(w, 200, item(seg[1], "Blue Shirt"))
			case 3:
				writeJSON(w, 200, map[string]any{"ok": true, "item_id": seg[1], "action": seg[2]})
			default:
				http.NotFound(w, r)
			}
		case "outfits":
			switch len(seg) {
			case 1:
				writeJSON(w, 200, map[string]any{"outfits": []any{outfit("outfit-1"), outfit("outfit-2")}})
			case 2:
				switch seg[1] {
				case "studio":
					writeJSON(w, 201, outfit("outfit-created"))
				case "suggest":
					writeJSON(w, 200, outfit("outfit-suggested"))
				default:
					if r.Method == http.MethodDelete {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					writeJSON(w, 200, outfit(seg[1]))
				}
			case 3:
				writeJSON(w, 200, map[string]any{"ok": true, "outfit_id": seg[1], "status": seg[2]})
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, backendURL string, opts ...func(*Server)) *client.Client {
	t.Helper()
	cfg := config.Config{
		Transport: config.TransportStdio, AuthMode: config.AuthDev,
		WardrowbeURL: backendURL, ExternalID: "test-user", ExternalEmail: "t@example.com",
		ImageVariant: config.VariantMedium, ImageMaxDim: 768, MaxConcurrent: 16,
	}
	provider := wardrowbe.DevTokenProvider{ExternalID: cfg.ExternalID, Email: cfg.ExternalEmail}
	wc := wardrowbe.NewClient(backendURL, provider, &http.Client{Timeout: 10 * time.Second}, slog.Default())
	srv := New(cfg, wc, slog.Default())
	for _, opt := range opts {
		opt(srv)
	}

	c, err := client.NewInProcessClient(srv.MCP())
	if err != nil {
		t.Fatalf("in-process client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start client: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = "2024-11-05"
	initReq.Params.ClientInfo = mcp.Implementation{Name: "integration", Version: "1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

func call(t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	return res
}

func firstText(res *mcp.CallToolResult) string {
	for _, ct := range res.Content {
		if tc, ok := ct.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func TestAllToolsListed(t *testing.T) {
	c := newTestClient(t, mockBackend(t).URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lt, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(lt.Tools) != expectedToolCount {
		t.Fatalf("tool count = %d, want %d", len(lt.Tools), expectedToolCount)
	}
}

func TestToolsHappyPath(t *testing.T) {
	c := newTestClient(t, mockBackend(t).URL)

	cases := []struct {
		name      string
		args      map[string]any
		wantImage bool
	}{
		{name: "health"},
		{name: "auth_config"},
		{name: "session_info"},
		{name: "get_wardrobe_summary"},
		{name: "get_most_worn_items", args: map[string]any{"limit": 3}},
		{name: "recent_notifications", args: map[string]any{"limit": 5}},
		{name: "test_notification", args: map[string]any{"setting_id": "setting-1"}},
		{name: "list_items", args: map[string]any{"page": 1, "page_size": 10, "category": "shirt", "search": "blue"}},
		{name: "get_item", args: map[string]any{"item_id": "item-1"}},
		{name: "get_items_to_wash", args: map[string]any{"limit": 5}},
		{name: "log_wear", args: map[string]any{"item_id": "item-1", "date": "2026-06-01"}},
		{name: "log_wash", args: map[string]any{"item_id": "item-1"}},
		{name: "archive_item", args: map[string]any{"item_id": "item-1", "reason": "worn out"}},
		{name: "restore_item", args: map[string]any{"item_id": "item-1"}},
		{name: "suggest_outfit", args: map[string]any{"occasion": "casual", "time_of_day": "morning", "notes": "light"}},
		{name: "create_outfit", args: map[string]any{"item_ids": []any{"item-1", "item-2"}, "occasion": "casual", "name": "Test Fit"}},
		{name: "get_latest_outfit"},
		{name: "get_outfit", args: map[string]any{"outfit_id": "outfit-1"}},
		{name: "delete_outfit", args: map[string]any{"outfit_id": "outfit-1"}},
		{name: "get_recent_outfits", args: map[string]any{"limit": 5, "status": "accepted"}},
		{name: "accept_latest_outfit"},
		{name: "reject_latest_outfit"},
		{name: "skip_latest_outfit"},
		{name: "submit_outfit_feedback", args: map[string]any{"outfit_id": "outfit-1", "rating": 5, "wore": true, "notes": "great"}},
		{name: "get_item_image", args: map[string]any{"item_id": "item-1", "variant": "medium"}, wantImage: true},
		{name: "get_outfit_images", args: map[string]any{"outfit_id": "outfit-1", "variant": "medium"}, wantImage: true},
		{name: "update_item", args: map[string]any{"item_id": "item-1", "name": "New", "primary_color": "navy", "colors": []any{"navy", "white"}, "wash_interval": 3, "favorite": true}},
		{name: "set_item_tags", args: map[string]any{"item_id": "item-1", "colors": []any{"blue"}, "pattern": "solid", "material": "cotton", "style": []any{"smart-casual"}, "season": []any{"summer"}, "formality": "casual", "fit": "slim"}},
		{name: "set_item_description", args: map[string]any{"item_id": "item-1", "description": "A nice blue shirt"}},
		{name: "create_item_from_base64", args: map[string]any{"image_base64": tinyPNG, "filename": "shirt.png", "name": "Uploaded", "type": "shirt"}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res := call(t, c, tt.name, tt.args)
			if res.IsError {
				t.Fatalf("tool error: %s", firstText(res))
			}
			if len(res.Content) == 0 {
				t.Fatal("empty content")
			}
			if tt.wantImage {
				hasImg := false
				for _, ct := range res.Content {
					if _, ok := ct.(mcp.ImageContent); ok {
						hasImg = true
					}
				}
				if !hasImg {
					t.Fatal("expected image content, got none")
				}
			}
		})
	}
}

// TestCreateItemFromURL covers the external-fetch tool against the loopback mock
// by swapping the SSRF-guarded transport for a plain one (the guard itself is
// covered by TestToolGuards). The upload path it shares with the base64 tool is
// thus exercised end-to-end without outbound network.
func TestCreateItemFromURL(t *testing.T) {
	backend := mockBackend(t)
	// Inject a plain transport on this server instance so the in-process fetch can
	// reach the loopback mock backend. The override is per-instance (no shared
	// package state), so this is race-free and does not constrain t.Parallel.
	c := newTestClient(t, backend.URL, func(s *Server) {
		s.imageTransport = func() *http.Transport { return &http.Transport{} }
	})

	res := call(t, c, "create_item_from_url", map[string]any{
		"image_url": backend.URL + "/media/x.png", "name": "From URL",
	})
	if res.IsError {
		t.Fatalf("tool error: %s", firstText(res))
	}
	if !strings.Contains(firstText(res), "item-new") {
		t.Fatalf("unexpected result: %s", firstText(res))
	}
}

// TestToolGuards asserts the validation/SSRF guards reject bad input. These run
// with the real SSRF-guarded transport (the real ssrfTransport is used).
func TestToolGuards(t *testing.T) {
	c := newTestClient(t, mockBackend(t).URL)

	cases := []struct {
		name string
		args map[string]any
		want string // substring expected in the error text
	}{
		{"create_item_from_url", map[string]any{"image_url": "http://127.0.0.1:1/x.png"}, "non-public"},
		{"create_item_from_url", map[string]any{"image_url": "file:///etc/passwd"}, "http(s)"},
		{"suggest_outfit", map[string]any{"occasion": "spacewalk"}, "invalid occasion"},
		{"create_outfit", map[string]any{"item_ids": []any{"item-1", ""}, "occasion": "casual"}, "empty values"},
		{"log_wear", map[string]any{"item_id": "item-1", "date": "06-2026"}, "YYYY-MM-DD"},
		{"get_item", map[string]any{}, "item_id is required"},
		{"create_item_from_base64", map[string]any{"image_base64": base64.StdEncoding.EncodeToString([]byte("not-an-image"))}, "not an image"},
	}
	for _, tt := range cases {
		t.Run(tt.name+"_"+tt.want, func(t *testing.T) {
			res := call(t, c, tt.name, tt.args)
			if !res.IsError {
				t.Fatalf("expected tool error, got success: %s", firstText(res))
			}
			if !strings.Contains(firstText(res), tt.want) {
				t.Fatalf("error %q does not contain %q", firstText(res), tt.want)
			}
		})
	}
}
