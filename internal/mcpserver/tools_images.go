package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

// maxOutfitImageFanout caps how many garment images one get_outfit_images call
// fetches and returns — both backend fan-out and response size grow per image.
const maxOutfitImageFanout = 20

func (s *Server) registerImageTools() {
	s.add(mcp.NewTool("wardrowbe_get_item_image",
		mcp.WithDescription("Return a garment photo so you can see and analyze it directly. "+
			"Each image costs vision tokens; default variant is configured server-side."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("variant", mcp.Description("Image size: thumb, medium, or full."),
			mcp.Enum("thumb", "medium", "full")),
	), s.handleItemImage)

	s.add(mcp.NewTool("wardrowbe_get_outfit_images",
		mcp.WithDescription("Return one photo per garment in an outfit (max 20), plus a small JSON "+
			"manifest. Each image is preceded by a text block carrying its item_id. Only the garments "+
			"in this outfit are returned; each image costs vision tokens."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Required(), mcp.Description("Outfit id.")),
		mcp.WithString("variant", mcp.Description("Image size: thumb, medium, or full."),
			mcp.Enum("thumb", "medium", "full")),
	), s.handleOutfitImages)
}

func (s *Server) handleItemImage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, errRes := requireID(req, "item_id")
	if errRes != nil {
		return errRes, nil
	}
	variant, errRes := s.variantOrDefault(req)
	if errRes != nil {
		return errRes, nil
	}

	img, err := s.client.ItemImage(ctx, itemID, variant, s.cfg.ImageMaxDim)
	if err != nil {
		return toolErr("get item image failed", err), nil
	}

	manifest, _ := json.Marshal(map[string]any{
		"item_id": itemID, "variant": variant, "mime_type": img.MIME, "bytes": len(img.Data),
	})
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(manifest)),
			mcp.NewImageContent(base64.StdEncoding.EncodeToString(img.Data), img.MIME),
		},
	}, nil
}

func (s *Server) handleOutfitImages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	outfitID, errRes := requireID(req, "outfit_id")
	if errRes != nil {
		return errRes, nil
	}
	variant, errRes := s.variantOrDefault(req)
	if errRes != nil {
		return errRes, nil
	}

	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits/"+url.PathEscape(outfitID), nil, nil)
	if err != nil {
		return toolErr("get outfit failed", err), nil
	}

	garments, err := extractOutfitGarments(raw)
	if err != nil {
		return toolErr("could not read outfit garments", err), nil
	}
	if len(garments) == 0 {
		return mcp.NewToolResultError("outfit has no garments with images"), nil
	}
	truncated := 0
	if len(garments) > maxOutfitImageFanout {
		truncated = len(garments) - maxOutfitImageFanout
		garments = garments[:maxOutfitImageFanout]
	}

	content := []mcp.Content{}
	manifest := make([]map[string]any, 0, len(garments))
	fetched := 0

	for _, g := range garments {
		itemID := wardrowbe.StringField(g, "id")
		img, err := s.client.ItemImageFromPayload(ctx, itemID, g, variant, s.cfg.ImageMaxDim)
		entry := map[string]any{"item_id": itemID}
		if err != nil {
			entry["error"] = safeErrText(err)
			manifest = append(manifest, entry)
			s.log.Warn("outfit image fetch failed", "item_id", itemID, "err", err)
			continue
		}
		entry["mime_type"] = img.MIME
		entry["bytes"] = len(img.Data)
		manifest = append(manifest, entry)
		// Tag each image with its item_id so the caller doesn't have to align
		// the Nth image with the Nth manifest entry by position.
		label, _ := json.Marshal(map[string]string{"item_id": itemID})
		content = append(content,
			mcp.NewTextContent(string(label)),
			mcp.NewImageContent(base64.StdEncoding.EncodeToString(img.Data), img.MIME))
		fetched++
	}

	if fetched == 0 {
		// Every garment image failed — surface it as an error, not a "success"
		// with an all-errors manifest the model would misread as a partial result.
		return mcp.NewToolResultError("could not fetch any garment images for this outfit"), nil
	}

	headerFields := map[string]any{
		"outfit_id": outfitID, "variant": variant, "garments": manifest,
	}
	if truncated > 0 {
		headerFields["truncated_garments"] = truncated
	}
	header, _ := json.Marshal(headerFields)
	// manifest text first, then the labelled images
	return &mcp.CallToolResult{
		Content: append([]mcp.Content{mcp.NewTextContent(string(header))}, content...),
	}, nil
}

// variantOrDefault resolves the requested image variant, rejecting unknown
// values instead of silently falling back to medium (the manifest would then
// report a variant that was never fetched).
func (s *Server) variantOrDefault(req mcp.CallToolRequest) (string, *mcp.CallToolResult) {
	v := req.GetString("variant", "")
	if v == "" {
		return string(s.cfg.ImageVariant), nil
	}
	switch v {
	case "thumb", "medium", "full":
		return v, nil
	}
	return "", mcp.NewToolResultErrorf("invalid variant %q (want thumb, medium or full)", v)
}

// extractOutfitGarments pulls the garment item objects out of an outfit payload,
// tolerating the common wrapping keys.
func extractOutfitGarments(raw json.RawMessage) ([]map[string]any, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for _, key := range []string{"items", "garments", "pieces"} {
		if v, ok := obj[key]; ok {
			var arr []map[string]any
			if err := json.Unmarshal(v, &arr); err == nil {
				return arr, nil
			}
		}
	}
	// No recognised garment-list key: a well-formed but garment-less outfit.
	// Return an empty list (not an error) and let the caller's len==0 guard produce
	// the user-facing "no garments" message — only malformed JSON is a real error.
	return nil, nil
}
