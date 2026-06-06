package mcpserver

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerOutfitTools() {
	s.add(mcp.NewTool("suggest_outfit",
		mcp.WithDescription("Ask the in-cluster model to generate an outfit suggestion. NOTE: that "+
			"model is intentionally weak — prefer composing the outfit yourself (use list_items + "+
			"get_item_image to see garments) and persisting it with create_outfit."),
		mcp.WithString("occasion", mcp.Description("Occasion."), mcp.Enum(occasionList()...)),
		mcp.WithString("time_of_day", mcp.Description("Time of day."), mcp.Enum(timeOfDayList()...)),
		mcp.WithString("target_date", mcp.Description("Target date, YYYY-MM-DD.")),
		mcp.WithString("notes", mcp.Description("Free-text styling notes/constraints.")),
	), s.handleSuggestOutfit)

	s.add(mcp.NewTool("create_outfit",
		mcp.WithDescription("Persist an outfit YOU composed, directly from explicit item ids "+
			"(POST /outfits/studio). Use this — not suggest_outfit — when you have chosen the "+
			"garments yourself: it saves your pick to Wardrowbe without delegating to the weak "+
			"in-cluster model. Provide 1-20 item ids (from list_items / get_item / get_item_image)."),
		mcp.WithArray("item_ids", mcp.Required(),
			mcp.Description("Chosen item ids, 1-20."), mcp.WithStringItems()),
		mcp.WithString("occasion", mcp.Required(),
			mcp.Description("Occasion (must be one of the supported values)."), mcp.Enum(occasionList()...)),
		mcp.WithString("name", mcp.Description("Optional outfit name (up to 100 chars).")),
		mcp.WithString("scheduled_for", mcp.Description("Optional date to schedule it for, YYYY-MM-DD.")),
		mcp.WithBoolean("mark_worn", mcp.Description("Also mark the outfit (and its items) worn now. Default false.")),
		mcp.WithString("source_item_id", mcp.Description("Optional seed item id this outfit was built around.")),
	), s.handleCreateOutfit)

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

const (
	maxOutfitItems   = 20
	maxOutfitNameLen = 100
)

func (s *Server) handleCreateOutfit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemIDs, err := req.RequireStringSlice("item_ids")
	if err != nil || len(itemIDs) == 0 {
		return mcp.NewToolResultError("item_ids is required (1-20 item ids)"), nil
	}
	if len(itemIDs) > maxOutfitItems {
		return mcp.NewToolResultErrorf("too many items: %d (max %d)", len(itemIDs), maxOutfitItems), nil
	}

	occasion, err := req.RequireString("occasion")
	if err != nil || occasion == "" {
		return mcp.NewToolResultError("occasion is required"), nil
	}
	if _, ok := validOccasions[occasion]; !ok {
		return mcp.NewToolResultErrorf("invalid occasion %q (must be one of: %s)",
			occasion, strings.Join(occasionList(), ", ")), nil
	}

	outfit := wardrowbe.StudioOutfit{
		Items:    itemIDs,
		Occasion: occasion,
		MarkWorn: req.GetBool("mark_worn", false),
	}
	if name := req.GetString("name", ""); name != "" {
		if len(name) > maxOutfitNameLen {
			return mcp.NewToolResultErrorf("name too long: %d chars (max %d)", len(name), maxOutfitNameLen), nil
		}
		outfit.Name = &name
	}
	if d := req.GetString("scheduled_for", ""); d != "" {
		outfit.ScheduledFor = &d
	}
	if sid := req.GetString("source_item_id", ""); sid != "" {
		outfit.SourceItemID = &sid
	}

	raw, err := s.client.CreateStudioOutfit(ctx, outfit)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("create outfit failed", err), nil
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
