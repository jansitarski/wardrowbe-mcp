package mcpserver

import (
	"context"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerWritebackTools() {
	s.add(mcp.NewTool("wardrowbe_update_item",
		mcp.WithDescription("Update an item's attributes (PATCH /items/{id}). Only provided fields "+
			"change; empty strings/arrays are ignored, so fields cannot be cleared here (use "+
			"wardrowbe_set_item_description to clear the description)."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("type", mcp.Description("Item type (e.g. shirt, trousers).")),
		mcp.WithString("subtype", mcp.Description("Item subtype.")),
		mcp.WithString("name", mcp.Description("Display name.")),
		mcp.WithString("brand", mcp.Description("Brand.")),
		mcp.WithString("notes", mcp.Description("Free-text notes/description.")),
		mcp.WithBoolean("favorite", mcp.Description("Mark as favorite.")),
		mcp.WithString("primary_color", mcp.Description("Primary color.")),
		mcp.WithArray("colors", mcp.Description("All colors."), mcp.WithStringItems()),
		mcp.WithInteger("wash_interval", mcp.Description("Wears between washes."), mcp.Min(1)),
	), s.handleUpdateItem)

	s.add(mcp.NewTool("wardrowbe_set_item_tags",
		mcp.WithDescription("Set an item's structured attribute tags (PATCH /items/{id} tags). "+
			"Use after viewing the garment image to record accurate attributes."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithArray("colors", mcp.Description("Colors."), mcp.WithStringItems()),
		mcp.WithString("primary_color", mcp.Description("Primary color.")),
		mcp.WithString("pattern", mcp.Description("Pattern (e.g. solid, striped, check).")),
		mcp.WithString("material", mcp.Description("Material (e.g. linen, cotton, wool).")),
		mcp.WithArray("style", mcp.Description("Styles (e.g. smart-casual)."), mcp.WithStringItems()),
		mcp.WithArray("season", mcp.Description("Seasons (e.g. spring, summer)."), mcp.WithStringItems()),
		mcp.WithString("formality", mcp.Description("Formality (e.g. casual, smart-casual, formal).")),
		mcp.WithString("fit", mcp.Description("Fit (e.g. slim, regular, relaxed).")),
	), s.handleSetItemTags)

	s.add(mcp.NewTool("wardrowbe_set_item_description",
		mcp.WithDescription("Set an item's free-text description (same field wardrowbe_update_item "+
			"calls notes). Unlike wardrowbe_update_item, an empty string is accepted and clears the "+
			"description."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("description", mcp.Required(), mcp.Description("Description text (empty clears it).")),
	), s.handleSetItemDescription)
}

func (s *Server) handleUpdateItem(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}

	args := req.GetArguments()
	patch := wardrowbe.ItemUpdate{}
	setIfPresent(args, "type", func(v string) { patch.Type = &v })
	setIfPresent(args, "subtype", func(v string) { patch.Subtype = &v })
	setIfPresent(args, "name", func(v string) { patch.Name = &v })
	setIfPresent(args, "brand", func(v string) { patch.Brand = &v })
	setIfPresent(args, "notes", func(v string) { patch.Notes = &v })
	setIfPresent(args, "primary_color", func(v string) { patch.PrimaryColor = &v })
	if fav, present, errRes := argBool(req, "favorite"); errRes != nil {
		return errRes, nil
	} else if present {
		patch.Favorite = &fav
	}
	if wi, present, errRes := argInt(req, "wash_interval"); errRes != nil {
		return errRes, nil
	} else if present {
		if wi < 1 {
			return mcp.NewToolResultErrorf("wash_interval must be >= 1 (got %d)", wi), nil
		}
		patch.WashInterval = &wi
	}
	if colors := req.GetStringSlice("colors", nil); len(colors) > 0 {
		patch.Colors = colors
	}

	if isEmptyUpdate(patch) {
		return mcp.NewToolResultError("no fields provided to update"), nil
	}

	raw, err := s.client.UpdateItem(ctx, itemID, patch)
	if err != nil {
		return toolErr("update item failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleSetItemTags(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}

	args := req.GetArguments()
	tags := wardrowbe.ItemTags{}
	hasField := false

	if colors := req.GetStringSlice("colors", nil); len(colors) > 0 {
		tags.Colors = colors
		hasField = true
	}
	if style := req.GetStringSlice("style", nil); len(style) > 0 {
		tags.Style = style
		hasField = true
	}
	if season := req.GetStringSlice("season", nil); len(season) > 0 {
		tags.Season = season
		hasField = true
	}
	setIfPresent(args, "primary_color", func(v string) { tags.PrimaryColor = &v; hasField = true })
	setIfPresent(args, "pattern", func(v string) { tags.Pattern = &v; hasField = true })
	setIfPresent(args, "material", func(v string) { tags.Material = &v; hasField = true })
	setIfPresent(args, "formality", func(v string) { tags.Formality = &v; hasField = true })
	setIfPresent(args, "fit", func(v string) { tags.Fit = &v; hasField = true })

	if !hasField {
		return mcp.NewToolResultError("no tag fields provided"), nil
	}

	raw, err := s.client.UpdateItem(ctx, itemID, wardrowbe.ItemUpdate{Tags: &tags})
	if err != nil {
		return toolErr("set item tags failed", err), nil
	}
	return jsonText(raw), nil
}

func (s *Server) handleSetItemDescription(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}
	desc, err := req.RequireString("description")
	if err != nil {
		return mcp.NewToolResultError("description is required"), nil
	}
	raw, err := s.client.UpdateItem(ctx, itemID, wardrowbe.ItemUpdate{Notes: &desc})
	if err != nil {
		return toolErr("set item description failed", err), nil
	}
	return jsonText(raw), nil
}

// setIfPresent calls set with the string arg if it is present and non-empty.
func setIfPresent(args map[string]any, key string, set func(string)) {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			set(s)
		}
	}
}

func isEmptyUpdate(p wardrowbe.ItemUpdate) bool {
	return p.Type == nil && p.Subtype == nil && p.Name == nil && p.Brand == nil &&
		p.Notes == nil && p.Favorite == nil && p.PrimaryColor == nil &&
		p.WashInterval == nil && p.Tags == nil && len(p.Colors) == 0
}
