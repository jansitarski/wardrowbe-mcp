package mcpserver

import (
	"context"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerOutfitTools() {
	s.add(mcp.NewTool("suggest_outfit",
		mcp.WithDescription("Request an outfit suggestion for an occasion and time of day."),
		mcp.WithString("occasion", mcp.Description("Occasion."), mcp.Enum(occasionList()...)),
		mcp.WithString("time_of_day", mcp.Description("Time of day."), mcp.Enum(timeOfDayList()...)),
		mcp.WithString("target_date", mcp.Description("Target date, YYYY-MM-DD.")),
		mcp.WithString("notes", mcp.Description("Free-text styling notes/constraints.")),
	), s.handleSuggestOutfit)

	s.add(mcp.NewTool("get_latest_outfit",
		mcp.WithDescription("Get the most recent outfit."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.handleLatestOutfit)

	s.add(mcp.NewTool("get_outfit",
		mcp.WithDescription("Get a single outfit by id."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Required(), mcp.Description("Outfit id.")),
	), s.handleGetOutfit)

	s.add(mcp.NewTool("get_recent_outfits",
		mcp.WithDescription("List recent outfits, optionally filtered by status."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("Max outfits (1-20)."), mcp.Min(1), mcp.Max(20)),
		mcp.WithString("status", mcp.Description("Filter by status (e.g. accepted, rejected, skipped, pending).")),
	), s.handleRecentOutfits)

	s.add(mcp.NewTool("accept_latest_outfit",
		mcp.WithDescription("Accept the most recent outfit."),
	), s.latestOutfitAction("accept"))

	s.add(mcp.NewTool("reject_latest_outfit",
		mcp.WithDescription("Reject the most recent outfit."),
	), s.latestOutfitAction("reject"))

	s.add(mcp.NewTool("skip_latest_outfit",
		mcp.WithDescription("Skip the most recent outfit."),
	), s.latestOutfitAction("skip"))

	s.add(mcp.NewTool("submit_outfit_feedback",
		mcp.WithDescription("Submit feedback for an outfit (rating, whether worn, notes)."),
		mcp.WithString("outfit_id", mcp.Required(), mcp.Description("Outfit id.")),
		mcp.WithInteger("rating", mcp.Description("Rating 1-5."), mcp.Min(1), mcp.Max(5)),
		mcp.WithBoolean("wore", mcp.Description("Whether the outfit was actually worn.")),
		mcp.WithString("notes", mcp.Description("Free-text feedback.")),
	), s.handleOutfitFeedback)
}

func (s *Server) handleSuggestOutfit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	body := map[string]any{}

	if occasion := req.GetString("occasion", ""); occasion != "" {
		if _, ok := validOccasions[occasion]; !ok {
			return mcp.NewToolResultErrorf("invalid occasion %q", occasion), nil
		}
		body["occasion"] = occasion
	}
	if tod := req.GetString("time_of_day", ""); tod != "" {
		if _, ok := validTimesOfDay[tod]; !ok {
			return mcp.NewToolResultErrorf("invalid time_of_day %q", tod), nil
		}
		body["time_of_day"] = tod
	}
	if d := req.GetString("target_date", ""); d != "" {
		body["target_date"] = d
	}
	if notes := req.GetString("notes", ""); notes != "" {
		body["notes"] = notes
	}

	raw, err := s.client.Request(ctx, http.MethodPost, "/outfits/suggest", nil, body)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("outfit suggestion failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleLatestOutfit(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := s.firstOutfitID(ctx)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("no latest outfit", err), nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("get latest outfit failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleGetOutfit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	outfitID, err := req.RequireString("outfit_id")
	if err != nil {
		return mcp.NewToolResultError("outfit_id is required"), nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits/"+url.PathEscape(outfitID), nil, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("get outfit failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleRecentOutfits(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := clampInt(req.GetInt("limit", 10), 1, 20)
	q := url.Values{"limit": {itoa(limit)}}
	if status := req.GetString("status", ""); status != "" {
		q.Set("status", status)
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits", q, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("recent outfits fetch failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) latestOutfitAction(action string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := s.firstOutfitID(ctx)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("no latest outfit", err), nil
		}
		path := "/outfits/" + url.PathEscape(id) + "/" + action
		raw, err := s.client.Request(ctx, http.MethodPost, path, nil, nil)
		if err != nil {
			return mcp.NewToolResultErrorFromErr(action+" outfit failed", err), nil
		}
		return jsonText(raw), nil
	}
}

func (s *Server) handleOutfitFeedback(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	outfitID, err := req.RequireString("outfit_id")
	if err != nil {
		return mcp.NewToolResultError("outfit_id is required"), nil
	}
	body := map[string]any{}
	if _, ok := req.GetArguments()["rating"]; ok {
		body["rating"] = clampInt(req.GetInt("rating", 3), 1, 5)
	}
	if _, ok := req.GetArguments()["wore"]; ok {
		body["wore"] = req.GetBool("wore", false)
	}
	if notes := req.GetString("notes", ""); notes != "" {
		body["notes"] = notes
	}

	path := "/outfits/" + url.PathEscape(outfitID) + "/feedback"
	raw, err := s.client.Request(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("submit feedback failed", err), nil
	}
	return jsonText(raw), nil
}
