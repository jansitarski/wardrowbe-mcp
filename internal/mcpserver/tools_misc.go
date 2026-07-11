package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

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

	s.add(mcp.NewTool("wardrowbe_get_capabilities",
		mcp.WithDescription("Backend capability flags (GET /capabilities): whether the internal AI "+
			"capabilities are active (ai.vision, ai.text) and which external write surfaces exist "+
			"(features.external_tagging/suggestions/pairings). Check this to decide whether YOU own "+
			"tagging, suggestions and pairings (author them via the wardrowbe_create_* and "+
			"wardrowbe_set_item_tags tools) or the in-cluster model does."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/capabilities"))

	s.add(mcp.NewTool("wardrowbe_get_preferences",
		mcp.WithDescription("The user's styling preferences (GET /users/me/preferences): favorite and "+
			"avoided colors, style profile, default occasion, temperature sensitivity, layering and "+
			"variety settings, excluded items. Read these before authoring outfits so suggestions "+
			"respect the user's constraints."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/users/me/preferences"))

	s.add(mcp.NewTool("wardrowbe_get_learning_insights",
		mcp.WithDescription("The learned feedback profile (GET /learning): color/style scores, best "+
			"item pairs and active insights derived from outfit feedback history. Useful signal for "+
			"authoring suggestions and pairings the user is likely to accept."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.simpleGet("/learning"))

	s.add(mcp.NewTool("wardrowbe_get_item_pair_suggestions",
		mcp.WithDescription("Items that historically pair well with a given item "+
			"(GET /learning/item-pairs/{id}), based on positive outfit feedback. Good input for "+
			"wardrowbe_create_item_pairing."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithInteger("limit", mcp.Description("Max pairs (1-20)."),
			mcp.DefaultNumber(5), mcp.Min(1), mcp.Max(20)),
	), s.handleItemPairSuggestions)

	s.add(mcp.NewTool("wardrowbe_get_weather",
		mcp.WithDescription("Current weather at the user's configured location (GET /weather/current). "+
			"The internal generator conditions outfits on weather — do the same when authoring "+
			"suggestions. Pass latitude/longitude only to override the stored location."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithNumber("latitude", mcp.Description("Optional latitude override."), mcp.Min(-90), mcp.Max(90)),
		mcp.WithNumber("longitude", mcp.Description("Optional longitude override."), mcp.Min(-180), mcp.Max(180)),
	), s.handleWeather("/weather/current", false))

	s.add(mcp.NewTool("wardrowbe_get_weather_forecast",
		mcp.WithDescription("Daily weather forecast (GET /weather/forecast) — for planning outfits "+
			"ahead (pair with scheduled_for on the authoring tools)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithNumber("latitude", mcp.Description("Optional latitude override."), mcp.Min(-90), mcp.Max(90)),
		mcp.WithNumber("longitude", mcp.Description("Optional longitude override."), mcp.Min(-180), mcp.Max(180)),
		mcp.WithInteger("days", mcp.Description("Days ahead (1-16)."),
			mcp.DefaultNumber(7), mcp.Min(1), mcp.Max(16)),
	), s.handleWeather("/weather/forecast", true))
}

// handleWeather serves both weather endpoints; withDays adds the forecast's
// days parameter.
func (s *Server) handleWeather(path string, withDays bool) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := url.Values{}
		for _, key := range []string{"latitude", "longitude"} {
			v, present, errRes := argFloat(req, key)
			if errRes != nil {
				return errRes, nil
			}
			if present {
				q.Set(key, strconv.FormatFloat(v, 'f', -1, 64))
			}
		}
		if withDays {
			days, errRes := argIntDefault(req, "days", 7, 1, 16)
			if errRes != nil {
				return errRes, nil
			}
			q.Set("days", itoa(days))
		}
		raw, err := s.client.Request(ctx, http.MethodGet, path, q, nil)
		if err != nil {
			return toolErr("weather fetch failed", err), nil
		}
		return jsonText(raw), nil
	}
}

func (s *Server) handleItemPairSuggestions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}
	limit, errRes := argIntDefault(req, "limit", 5, 1, 20)
	if errRes != nil {
		return errRes, nil
	}
	path := "/learning/item-pairs/" + url.PathEscape(itemID)
	raw, err := s.client.Request(ctx, http.MethodGet, path, url.Values{"limit": {itoa(limit)}}, nil)
	if err != nil {
		return toolErr("item pair suggestions fetch failed", err), nil
	}
	return jsonText(raw), nil
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
