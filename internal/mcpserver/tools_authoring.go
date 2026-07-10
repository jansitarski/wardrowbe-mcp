package mcpserver

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

// Authoring tools (jansitarski/wardrowbe#3): the write surface an agent uses to
// persist the outfits the backend's internal text model would otherwise
// generate. Rows land as Outfit(source=external).

const (
	maxAuthoringTextLen = 2000 // backend cap for reasoning/style_notes/notes
	maxPaletteColors    = 10
	maxPaletteColorLen  = 50
)

func (s *Server) registerAuthoringTools() {
	suggestionOpts := []mcp.ToolOption{
		mcp.WithDescription("Persist an outfit suggestion YOU authored from explicit item ids " +
			"(POST /outfits/suggestions). It lands as a pending suggestion (source=external) the " +
			"user accepts or rejects in the app — the agent-side replacement for the weak/disabled " +
			"in-cluster text model. Use wardrowbe_create_outfit instead when the user has already " +
			"committed to wearing the outfit. Item positions follow the item_ids order."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithArray("item_ids", mcp.Required(),
			mcp.Description("Chosen item ids, 1-20."), mcp.WithStringItems(), mcp.MinItems(1), mcp.MaxItems(maxOutfitItems)),
		mcp.WithString("occasion", mcp.Required(),
			mcp.Description("Occasion (must be one of the supported values)."), mcp.Enum(occasionList()...)),
		mcp.WithString("name", mcp.Description("Optional outfit name (up to 100 chars)."), mcp.MaxLength(maxOutfitNameLen)),
		mcp.WithString("scheduled_for", mcp.Description("Date the suggestion is for, YYYY-MM-DD (defaults to the user's today).")),
		mcp.WithString("reasoning", mcp.Description("Why this outfit works — shown as the suggestion's reasoning."),
			mcp.MaxLength(maxAuthoringTextLen)),
		mcp.WithString("style_notes", mcp.Description("Styling tips for wearing it."), mcp.MaxLength(maxAuthoringTextLen)),
	}
	s.add(mcp.NewTool("wardrowbe_create_outfit_suggestion",
		append(suggestionOpts, authoringAttributeOptions()...)...), s.handleCreateSuggestion)

	pairingOpts := []mcp.ToolOption{
		mcp.WithDescription("Persist a pairing YOU authored around a wardrobe item " +
			"(POST /pairings/item/{item_id}): an outfit built to go with that source item, listed " +
			"on the item's pairings page (source=external). The source item is included " +
			"automatically when absent from item_ids; positions follow your order."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Source item id the pairing is built around.")),
		mcp.WithArray("item_ids", mcp.Required(),
			mcp.Description("Partner item ids to pair with the source item, 1-20."),
			mcp.WithStringItems(), mcp.MinItems(1), mcp.MaxItems(maxOutfitItems)),
		mcp.WithString("scheduled_for", mcp.Description("Date the pairing is for, YYYY-MM-DD (defaults to the user's today).")),
		mcp.WithString("reasoning", mcp.Description("Why the pairing works — shown as its headline."),
			mcp.MaxLength(maxAuthoringTextLen)),
		mcp.WithString("style_notes", mcp.Description("Styling tip for wearing it."), mcp.MaxLength(maxAuthoringTextLen)),
	}
	s.add(mcp.NewTool("wardrowbe_create_item_pairing",
		append(pairingOpts, authoringAttributeOptions()...)...), s.handleCreatePairing)
}

// authoringAttributeOptions returns the tool options for the descriptive
// attributes shared by the authoring tools and wardrowbe_create_outfit.
func authoringAttributeOptions() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("season", mcp.Description("Season the outfit suits."), mcp.Enum(seasonList()...)),
		mcp.WithString("formality", mcp.Description("Formality level."), mcp.Enum(formalityList()...)),
		mcp.WithArray("palette", mcp.Description("Dominant outfit colors, most prominent first (max 10)."),
			mcp.WithStringItems(), mcp.MaxItems(maxPaletteColors)),
		mcp.WithString("notes", mcp.Description("Free-text notes about the outfit."), mcp.MaxLength(maxAuthoringTextLen)),
	}
}

// authoringAttributes parses the shared season/formality/palette/notes
// arguments; zero-value fields mean "not provided" and are omitted on the wire.
func authoringAttributes(req mcp.CallToolRequest) (wardrowbe.OutfitAttributes, *mcp.CallToolResult) {
	var attrs wardrowbe.OutfitAttributes

	if season := req.GetString("season", ""); season != "" {
		if _, ok := validSeasons[season]; !ok {
			return attrs, mcp.NewToolResultErrorf("invalid season %q (must be one of: %s)",
				season, strings.Join(seasonList(), ", "))
		}
		attrs.Season = &season
	}
	if formality := req.GetString("formality", ""); formality != "" {
		if _, ok := validFormalities[formality]; !ok {
			return attrs, mcp.NewToolResultErrorf("invalid formality %q (must be one of: %s)",
				formality, strings.Join(formalityList(), ", "))
		}
		attrs.Formality = &formality
	}
	if palette := req.GetStringSlice("palette", nil); len(palette) > 0 {
		if len(palette) > maxPaletteColors {
			return attrs, mcp.NewToolResultErrorf("too many palette colors: %d (max %d)", len(palette), maxPaletteColors)
		}
		for _, c := range palette {
			if trimmed := strings.TrimSpace(c); trimmed == "" || utf8.RuneCountInString(trimmed) > maxPaletteColorLen {
				return attrs, mcp.NewToolResultErrorf("palette colors must be 1-%d characters", maxPaletteColorLen)
			}
		}
		attrs.Palette = palette
	}
	notes, errRes := argText(req, "notes", maxAuthoringTextLen)
	if errRes != nil {
		return attrs, errRes
	}
	attrs.Notes = notes

	return attrs, nil
}

// argText reads an optional free-text argument, enforcing the backend's length
// cap at the gateway so the caller gets a clear message instead of a 422.
func argText(req mcp.CallToolRequest, key string, maxLen int) (*string, *mcp.CallToolResult) {
	v := req.GetString(key, "")
	if v == "" {
		return nil, nil
	}
	if n := utf8.RuneCountInString(v); n > maxLen {
		return nil, mcp.NewToolResultErrorf("%s too long: %d chars (max %d)", key, n, maxLen)
	}
	return &v, nil
}

// argItemIDs reads a required list of non-empty item ids (1-maxOutfitItems).
func argItemIDs(req mcp.CallToolRequest, key string) ([]string, *mcp.CallToolResult) {
	ids, err := req.RequireStringSlice(key)
	if err != nil || len(ids) == 0 {
		return nil, mcp.NewToolResultError(key + " is required (1-20 item ids)")
	}
	if len(ids) > maxOutfitItems {
		return nil, mcp.NewToolResultErrorf("too many items: %d (max %d)", len(ids), maxOutfitItems)
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, mcp.NewToolResultError(key + " must not contain empty values")
		}
	}
	return ids, nil
}

func (s *Server) handleCreateSuggestion(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemIDs, errRes := argItemIDs(req, "item_ids")
	if errRes != nil {
		return errRes, nil
	}
	occasion, err := req.RequireString("occasion")
	if err != nil || occasion == "" {
		return mcp.NewToolResultError("occasion is required"), nil
	}
	if _, ok := validOccasions[occasion]; !ok {
		return mcp.NewToolResultErrorf("invalid occasion %q (must be one of: %s)",
			occasion, strings.Join(occasionList(), ", ")), nil
	}

	suggestion := wardrowbe.AuthoredSuggestion{Items: itemIDs, Occasion: occasion}
	if name := req.GetString("name", ""); name != "" {
		if n := utf8.RuneCountInString(name); n > maxOutfitNameLen {
			return mcp.NewToolResultErrorf("name too long: %d chars (max %d)", n, maxOutfitNameLen), nil
		}
		suggestion.Name = &name
	}
	if d := req.GetString("scheduled_for", ""); d != "" {
		if !isValidDate(d) {
			return mcp.NewToolResultError("scheduled_for must be YYYY-MM-DD"), nil
		}
		suggestion.ScheduledFor = &d
	}
	var errRes2 *mcp.CallToolResult
	if suggestion.Reasoning, errRes2 = argText(req, "reasoning", maxAuthoringTextLen); errRes2 != nil {
		return errRes2, nil
	}
	if suggestion.StyleNotes, errRes2 = argText(req, "style_notes", maxAuthoringTextLen); errRes2 != nil {
		return errRes2, nil
	}
	attrs, errRes2 := authoringAttributes(req)
	if errRes2 != nil {
		return errRes2, nil
	}
	suggestion.OutfitAttributes = attrs

	raw, err := s.client.CreateSuggestion(ctx, suggestion)
	if err != nil {
		return toolErr("create outfit suggestion failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleCreatePairing(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}
	itemIDs, errRes := argItemIDs(req, "item_ids")
	if errRes != nil {
		return errRes, nil
	}

	pairing := wardrowbe.AuthoredPairing{Items: itemIDs}
	if d := req.GetString("scheduled_for", ""); d != "" {
		if !isValidDate(d) {
			return mcp.NewToolResultError("scheduled_for must be YYYY-MM-DD"), nil
		}
		pairing.ScheduledFor = &d
	}
	var errRes2 *mcp.CallToolResult
	if pairing.Reasoning, errRes2 = argText(req, "reasoning", maxAuthoringTextLen); errRes2 != nil {
		return errRes2, nil
	}
	if pairing.StyleNotes, errRes2 = argText(req, "style_notes", maxAuthoringTextLen); errRes2 != nil {
		return errRes2, nil
	}
	attrs, errRes2 := authoringAttributes(req)
	if errRes2 != nil {
		return errRes2, nil
	}
	pairing.OutfitAttributes = attrs

	raw, err := s.client.CreatePairing(ctx, itemID, pairing)
	if err != nil {
		return toolErr("create item pairing failed", err), nil
	}
	return jsonText(raw), nil
}
