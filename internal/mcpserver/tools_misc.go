package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerMiscTools() {
	s.add(mcp.NewTool("health",
		mcp.WithDescription("Check Wardrowbe backend health (GET /api/v1/health)."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.handleHealth)

	s.add(mcp.NewTool("auth_config",
		mcp.WithDescription("Get the backend auth configuration (GET /auth/config)."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/auth/config"))

	s.add(mcp.NewTool("session_info",
		mcp.WithDescription("Get the current backend session/user info (GET /auth/session)."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/auth/session"))

	s.add(mcp.NewTool("get_wardrobe_summary",
		mcp.WithDescription("Wardrobe analytics: counts, most/least/never worn, distributions."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/analytics"))

	s.add(mcp.NewTool("get_most_worn_items",
		mcp.WithDescription("Most-worn items, derived from analytics."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("How many items to return (1-10)."), mcp.Min(1), mcp.Max(10)),
	), s.handleMostWorn)

	s.add(mcp.NewTool("recent_notifications",
		mcp.WithDescription("Recent notification history (GET /notifications/history)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("How many notifications to return (1-100)."), mcp.Min(1), mcp.Max(100)),
	), s.handleRecentNotifications)

	s.add(mcp.NewTool("test_notification",
		mcp.WithDescription("Send a test notification for a notification setting."),
		mcp.WithString("setting_id", mcp.Required(), mcp.Description("Notification setting id.")),
	), s.handleTestNotification)
}

func (s *Server) handleHealth(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := s.client.Request(ctx, http.MethodGet, "/health", nil, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("health check failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleMostWorn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := clampInt(req.GetInt("limit", 5), 1, 10)
	raw, err := s.client.Request(ctx, http.MethodGet, "/analytics", nil, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("analytics fetch failed", err), nil
	}

	items := extractMostWorn(raw)
	if items == nil {
		// Structure not as expected — return the full analytics so the model can adapt.
		return jsonText(raw), nil
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return marshalText(map[string]any{"most_worn_items": items})
}

// extractMostWorn pulls a most-worn list out of the analytics payload, tolerating
// the several key names the backend may use. Returns nil if none is found.
func extractMostWorn(raw json.RawMessage) []json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	for _, key := range []string{"most_worn_items", "most_worn", "mostWorn", "most_worn_item"} {
		if v, ok := obj[key]; ok {
			var arr []json.RawMessage
			if err := json.Unmarshal(v, &arr); err == nil {
				return arr
			}
		}
	}
	return nil
}

func (s *Server) handleRecentNotifications(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := clampInt(req.GetInt("limit", 20), 1, 100)
	q := url.Values{"limit": {itoa(limit)}}
	raw, err := s.client.Request(ctx, http.MethodGet, "/notifications/history", q, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("notifications fetch failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleTestNotification(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	settingID, err := req.RequireString("setting_id")
	if err != nil {
		return mcp.NewToolResultError("setting_id is required"), nil
	}
	path := "/notifications/settings/" + url.PathEscape(settingID) + "/test"
	raw, err := s.client.Request(ctx, http.MethodPost, path, nil, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("test notification failed", err), nil
	}
	return jsonText(raw), nil
}

// simpleGet returns a handler that GETs a fixed backend path and returns the body.
func (s *Server) simpleGet(path string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw, err := s.client.Request(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("request failed", err), nil
		}
		return jsonText(raw), nil
	}
}
