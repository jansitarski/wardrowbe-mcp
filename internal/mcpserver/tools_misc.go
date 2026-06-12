package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerMiscTools() {
	s.add(mcp.NewTool("wardrowbe_health",
		mcp.WithDescription("Check Wardrowbe backend health (GET /api/v1/health)."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.handleHealth)

	s.add(mcp.NewTool("wardrowbe_auth_config",
		mcp.WithDescription("Get the backend auth configuration (GET /auth/config)."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/auth/config"))

	s.add(mcp.NewTool("wardrowbe_session_info",
		mcp.WithDescription("Get the current backend session/user info (GET /auth/session)."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/auth/session"))

	s.add(mcp.NewTool("wardrowbe_get_wardrobe_summary",
		mcp.WithDescription("Wardrobe analytics: counts, most/least/never worn, distributions."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/analytics"))

	s.add(mcp.NewTool("wardrowbe_get_most_worn_items",
		mcp.WithDescription("Most-worn items, derived from analytics."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("How many items to return (1-10)."),
			mcp.DefaultNumber(5), mcp.Min(1), mcp.Max(10)),
	), s.handleMostWorn)

	s.add(mcp.NewTool("wardrowbe_recent_notifications",
		mcp.WithDescription("Recent notification history (GET /notifications/history)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("How many notifications to return (1-100)."),
			mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(100)),
	), s.handleRecentNotifications)

	s.add(mcp.NewTool("wardrowbe_list_notification_settings",
		mcp.WithDescription("List the configured notification settings (GET /notifications/settings). "+
			"Use this to find the setting_id that wardrowbe_test_notification needs."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/notifications/settings"))

	s.add(mcp.NewTool("wardrowbe_test_notification",
		mcp.WithDescription("Send a test notification for a notification setting. Get the setting_id "+
			"from wardrowbe_list_notification_settings."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("setting_id", mcp.Required(), mcp.Description("Notification setting id.")),
	), s.handleTestNotification)
}

func (s *Server) handleHealth(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := s.client.Request(ctx, http.MethodGet, "/health", nil, nil)
	if err != nil {
		return toolErr("health check failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleMostWorn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit, errRes := argIntDefault(req, "limit", 5, 1, 10)
	if errRes != nil {
		return errRes, nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/analytics", nil, nil)
	if err != nil {
		return toolErr("analytics fetch failed", err), nil
	}

	items := extractMostWorn(raw)
	if items == nil {
		// The advertised top-N list isn't where we expect it. Fail clearly rather
		// than silently returning the entire (unsliced) analytics blob, which the
		// caller did not ask for and would misread as the most-worn list.
		return mcp.NewToolResultError(
			"backend analytics did not include a most-worn list; " +
				"use wardrowbe_get_wardrobe_summary for the full analytics payload"), nil
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return marshalText(map[string]any{"most_worn_items": items})
}

// extractMostWorn pulls a most-worn list out of the analytics payload, tolerating
// the several key names the backend may use. Returns nil if none is found; a
// present-but-null list means "no data yet" and comes back as an empty slice,
// not a missing-field error.
func extractMostWorn(raw json.RawMessage) []json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	for _, key := range []string{"most_worn_items", "most_worn", "mostWorn", "most_worn_item"} {
		if v, ok := obj[key]; ok {
			var arr []json.RawMessage
			if err := json.Unmarshal(v, &arr); err == nil {
				if arr == nil {
					return []json.RawMessage{}
				}
				return arr
			}
		}
	}
	return nil
}

func (s *Server) handleRecentNotifications(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit, errRes := argIntDefault(req, "limit", 20, 1, 100)
	if errRes != nil {
		return errRes, nil
	}
	q := url.Values{"limit": {itoa(limit)}}
	raw, err := s.client.Request(ctx, http.MethodGet, "/notifications/history", q, nil)
	if err != nil {
		return toolErr("notifications fetch failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleTestNotification(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	settingID, errRes := requireID(req, "setting_id")
	if errRes != nil {
		return errRes, nil
	}
	path := "/notifications/settings/" + url.PathEscape(settingID) + "/test"
	raw, err := s.client.Request(ctx, http.MethodPost, path, nil, nil)
	if err != nil {
		return toolErr("test notification failed", err), nil
	}
	return jsonText(raw), nil
}

// simpleGet returns a handler that GETs a fixed backend path and returns the body.
func (s *Server) simpleGet(path string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw, err := s.client.Request(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			return toolErr("request failed", err), nil
		}
		return jsonText(raw), nil
	}
}
