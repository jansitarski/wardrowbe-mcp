package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

// validOutfitStatuses mirrors the backend's outfit status values for the
// get_recent_outfits filter.
var validOutfitStatuses = map[string]struct{}{
	"pending": {}, "accepted": {}, "rejected": {}, "skipped": {},
}

func (s *Server) registerOutfitTools() {
	s.add(mcp.NewTool("wardrowbe_suggest_outfit",
		mcp.WithDescription("Ask the in-cluster model to generate an outfit suggestion. NOTE: that "+
			"model is intentionally weak — prefer composing the outfit yourself (use wardrowbe_list_items + "+
			"wardrowbe_get_item_image to see garments) and persisting it with wardrowbe_create_outfit."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("occasion", mcp.Description("Occasion."), mcp.Enum(occasionList()...)),
		mcp.WithString("time_of_day", mcp.Description("Time of day."), mcp.Enum(timeOfDayList()...)),
		mcp.WithString("target_date", mcp.Description("Target date, YYYY-MM-DD.")),
		mcp.WithString("notes", mcp.Description("Free-text styling notes/constraints.")),
	), s.handleSuggestOutfit)

	s.add(mcp.NewTool("wardrowbe_create_outfit",
		mcp.WithDescription("Persist an outfit YOU composed, directly from explicit item ids "+
			"(POST /outfits/studio). Use this — not wardrowbe_suggest_outfit — when you have chosen the "+
			"garments yourself: it saves your pick to Wardrowbe without delegating to the weak "+
			"in-cluster model. Provide 1-20 item ids (from wardrowbe_list_items / wardrowbe_get_item / "+
			"wardrowbe_get_item_image)."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithArray("item_ids", mcp.Required(),
			mcp.Description("Chosen item ids, 1-20."), mcp.WithStringItems(), mcp.MinItems(1), mcp.MaxItems(maxOutfitItems)),
		mcp.WithString("occasion", mcp.Required(),
			mcp.Description("Occasion (must be one of the supported values)."), mcp.Enum(occasionList()...)),
		mcp.WithString("name", mcp.Description("Optional outfit name (up to 100 chars)."), mcp.MaxLength(maxOutfitNameLen)),
		mcp.WithString("scheduled_for", mcp.Description("Optional date to schedule it for, YYYY-MM-DD.")),
		mcp.WithBoolean("mark_worn", mcp.Description("Also mark the outfit (and its items) worn now. Default false.")),
		mcp.WithString("source_item_id", mcp.Description("Optional seed item id this outfit was built around.")),
	), s.handleCreateOutfit)

	s.add(mcp.NewTool("wardrowbe_get_latest_outfit",
		mcp.WithDescription("Get the most recent outfit."),
		mcp.WithReadOnlyHintAnnotation(true),
	), s.handleLatestOutfit)

	s.add(mcp.NewTool("wardrowbe_get_outfit",
		mcp.WithDescription("Get a single outfit by id."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Required(), mcp.Description("Outfit id.")),
	), s.handleGetOutfit)

	s.add(mcp.NewTool("wardrowbe_delete_outfit",
		mcp.WithDescription("Permanently delete an outfit by id (DELETE /outfits/{id}). Works on ANY "+
			"outfit, not just the latest — get the id from wardrowbe_get_recent_outfits/wardrowbe_get_outfit. "+
			"This removes the outfit entirely; use wardrowbe_reject_latest_outfit/skip to merely dismiss a "+
			"suggestion."),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Required(), mcp.Description("Outfit id to delete.")),
	), s.handleDeleteOutfit)

	s.add(mcp.NewTool("wardrowbe_get_recent_outfits",
		mcp.WithDescription("List recent outfits, optionally filtered by status. Set compact=true "+
			"for a slim projection (id/name/status/occasion/scheduled_for/created_at + item id/type/name) — "+
			"roughly 20x smaller than the full payload; use it for dedupe/overview checks and fetch "+
			"individual outfits with wardrowbe_get_outfit when you need details."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithInteger("limit", mcp.Description("Max outfits (1-20)."),
			mcp.DefaultNumber(10), mcp.Min(1), mcp.Max(20)),
		mcp.WithString("status", mcp.Description("Filter by status."),
			mcp.Enum("pending", "accepted", "rejected", "skipped")),
		mcp.WithBoolean("compact", mcp.Description("Return the slim projection instead of full "+
			"outfit objects (which embed every item with signed image URLs). Default false.")),
	), s.handleRecentOutfits)

	s.add(mcp.NewTool("wardrowbe_accept_latest_outfit",
		mcp.WithDescription("Accept an outfit suggestion — the most recent one by default, or a "+
			"specific one via outfit_id. The result reports the outfit id acted on."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Description("Outfit id to accept (defaults to the most recent outfit).")),
	), s.latestOutfitAction("accept"))

	s.add(mcp.NewTool("wardrowbe_reject_latest_outfit",
		mcp.WithDescription("Reject an outfit suggestion — the most recent one by default, or a "+
			"specific one via outfit_id. The result reports the outfit id acted on."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Description("Outfit id to reject (defaults to the most recent outfit).")),
	), s.latestOutfitAction("reject"))

	s.add(mcp.NewTool("wardrowbe_skip_latest_outfit",
		mcp.WithDescription("Skip an outfit suggestion — the most recent one by default, or a "+
			"specific one via outfit_id. The result reports the outfit id acted on."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Description("Outfit id to skip (defaults to the most recent outfit).")),
	), s.latestOutfitAction("skip"))

	s.add(mcp.NewTool("wardrowbe_submit_outfit_feedback",
		mcp.WithDescription("Submit feedback for an outfit (rating, whether worn, notes)."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
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
		if !isValidDate(d) {
			return mcp.NewToolResultError("target_date must be YYYY-MM-DD"), nil
		}
		body["target_date"] = d
	}
	if notes := req.GetString("notes", ""); notes != "" {
		body["notes"] = notes
	}

	raw, err := s.client.Request(ctx, http.MethodPost, "/outfits/suggest", nil, body)
	if err != nil {
		return toolErr("outfit suggestion failed", err), nil
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
	for _, id := range itemIDs {
		if strings.TrimSpace(id) == "" {
			return mcp.NewToolResultError("item_ids must not contain empty values"), nil
		}
	}

	occasion, err := req.RequireString("occasion")
	if err != nil || occasion == "" {
		return mcp.NewToolResultError("occasion is required"), nil
	}
	if _, ok := validOccasions[occasion]; !ok {
		return mcp.NewToolResultErrorf("invalid occasion %q (must be one of: %s)",
			occasion, strings.Join(occasionList(), ", ")), nil
	}

	markWorn, _, errRes := argBool(req, "mark_worn")
	if errRes != nil {
		return errRes, nil
	}
	outfit := wardrowbe.StudioOutfit{
		Items:    itemIDs,
		Occasion: occasion,
		MarkWorn: markWorn,
	}
	if name := req.GetString("name", ""); name != "" {
		if n := utf8.RuneCountInString(name); n > maxOutfitNameLen {
			return mcp.NewToolResultErrorf("name too long: %d chars (max %d)", n, maxOutfitNameLen), nil
		}
		outfit.Name = &name
	}
	if d := req.GetString("scheduled_for", ""); d != "" {
		if !isValidDate(d) {
			return mcp.NewToolResultError("scheduled_for must be YYYY-MM-DD"), nil
		}
		outfit.ScheduledFor = &d
	}
	if sid := req.GetString("source_item_id", ""); sid != "" {
		outfit.SourceItemID = &sid
	}

	raw, err := s.client.CreateStudioOutfit(ctx, outfit)
	if err != nil {
		return toolErr("create outfit failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleLatestOutfit(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := s.firstOutfitID(ctx)
	if err != nil {
		return toolErr("no latest outfit", err), nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return toolErr("get latest outfit failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleGetOutfit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	outfitID, errRes := requireID(req, "outfit_id")
	if errRes != nil {
		return errRes, nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits/"+url.PathEscape(outfitID), nil, nil)
	if err != nil {
		return toolErr("get outfit failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleDeleteOutfit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	outfitID, errRes := requireID(req, "outfit_id")
	if errRes != nil {
		return errRes, nil
	}
	raw, err := s.client.DeleteOutfit(ctx, outfitID)
	if err != nil {
		return toolErr("delete outfit failed", err), nil
	}
	if raw != nil {
		return jsonText(raw), nil
	}
	return marshalText(map[string]any{"ok": true, "deleted_outfit_id": outfitID})
}

func (s *Server) handleRecentOutfits(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit, errRes := argIntDefault(req, "limit", 10, 1, 20)
	if errRes != nil {
		return errRes, nil
	}
	q := url.Values{"limit": {itoa(limit)}}
	if status := req.GetString("status", ""); status != "" {
		if _, ok := validOutfitStatuses[status]; !ok {
			return mcp.NewToolResultErrorf("invalid status %q (want pending, accepted, rejected or skipped)", status), nil
		}
		q.Set("status", status)
	}
	compact, _, errRes := argBool(req, "compact")
	if errRes != nil {
		return errRes, nil
	}
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits", q, nil)
	if err != nil {
		return toolErr("recent outfits fetch failed", err), nil
	}
	if !compact {
		return jsonText(raw), nil
	}
	return compactOutfitList(raw)
}

// compactOutfitItem / compactOutfit are field whitelists over the backend's
// OutfitListResponse: decoding drops everything not named here (reasoning,
// weather, feedback, per-item signed image URLs, ...), which is what shrinks
// the payload. json.Unmarshal ignores unknown fields by design.
type compactOutfitItem struct {
	ID   string  `json:"id"`
	Type string  `json:"type,omitempty"`
	Name *string `json:"name,omitempty"`
}

type compactOutfit struct {
	ID           string              `json:"id"`
	Name         *string             `json:"name,omitempty"`
	Status       string              `json:"status"`
	Occasion     string              `json:"occasion,omitempty"`
	ScheduledFor *string             `json:"scheduled_for,omitempty"`
	CreatedAt    string              `json:"created_at,omitempty"`
	Items        []compactOutfitItem `json:"items"`
}

type compactOutfitListPage struct {
	Outfits []compactOutfit `json:"outfits"`
	Total   *int            `json:"total,omitempty"`
	HasMore *bool           `json:"has_more,omitempty"`
}

// compactOutfitList projects the backend's full outfit list onto the compact
// shape. A full get_recent_outfits response at limit 20 runs to ~85k chars
// (every outfit embeds complete item objects with signed image URLs), which
// overwhelms tool-result token budgets when the caller only needs an overview.
func compactOutfitList(raw json.RawMessage) (*mcp.CallToolResult, error) {
	var page compactOutfitListPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return toolErr("compact projection failed (backend response did not match the expected shape)", err), nil
	}
	if page.Outfits == nil {
		page.Outfits = []compactOutfit{}
	}
	return marshalText(page)
}

func (s *Server) latestOutfitAction(action string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Resolving "latest" between the caller viewing an outfit and acting on
		// it is racy — a freshly generated outfit would silently receive the
		// action instead. An explicit outfit_id pins the target; either way the
		// resolved id is reported back so the caller can verify it.
		id := strings.TrimSpace(req.GetString("outfit_id", ""))
		if id == "" {
			var err error
			id, err = s.firstOutfitID(ctx)
			if err != nil {
				return toolErr("no latest outfit", err), nil
			}
		}
		path := "/outfits/" + url.PathEscape(id) + "/" + action
		raw, err := s.client.Request(ctx, http.MethodPost, path, nil, nil)
		if err != nil {
			return toolErr(action+" outfit failed", err), nil
		}
		if len(raw) == 0 {
			return marshalText(map[string]any{"ok": true, "action": action, "outfit_id": id})
		}
		return marshalText(map[string]any{"action": action, "outfit_id": id, "result": json.RawMessage(raw)})
	}
}

func (s *Server) handleOutfitFeedback(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	outfitID, errRes := requireID(req, "outfit_id")
	if errRes != nil {
		return errRes, nil
	}
	body := map[string]any{}
	if rating, present, errRes := argInt(req, "rating"); errRes != nil {
		return errRes, nil
	} else if present {
		if rating < 1 || rating > 5 {
			return mcp.NewToolResultErrorf("rating must be between 1 and 5 (got %d)", rating), nil
		}
		body["rating"] = rating
	}
	if wore, present, errRes := argBool(req, "wore"); errRes != nil {
		return errRes, nil
	} else if present {
		body["wore"] = wore
	}
	if notes := req.GetString("notes", ""); notes != "" {
		body["notes"] = notes
	}
	if len(body) == 0 {
		return mcp.NewToolResultError("provide at least one of: rating, wore, notes"), nil
	}

	path := "/outfits/" + url.PathEscape(outfitID) + "/feedback"
	raw, err := s.client.Request(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return toolErr("submit feedback failed", err), nil
	}
	return jsonText(raw), nil
}
