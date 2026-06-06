package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerImageTools() {
	s.add(mcp.NewTool("get_item_image",
		mcp.WithDescription("Return a garment photo so you can see and analyze it directly. "+
			"Each image costs vision tokens; default variant is configured server-side."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Item id.")),
		mcp.WithString("variant", mcp.Description("Image size: thumb, medium, or full."),
			mcp.Enum("thumb", "medium", "full")),
	), s.handleItemImage)

	s.add(mcp.NewTool("get_outfit_images",
		mcp.WithDescription("Return one photo per garment in an outfit, plus a small JSON manifest. "+
			"Only the garments in this outfit are returned; each image costs vision tokens."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("outfit_id", mcp.Required(), mcp.Description("Outfit id.")),
		mcp.WithString("variant", mcp.Description("Image size: thumb, medium, or full."),
			mcp.Enum("thumb", "medium", "full")),
	), s.handleOutfitImages)
}

func (s *Server) handleItemImage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	itemID, err := req.RequireString("item_id")
	if err != nil {
		return mcp.NewToolResultError("item_id is required"), nil
	}
	variant := s.variantOrDefault(req)

	img, err := s.client.ItemImage(ctx, itemID, variant, s.cfg.ImageMaxDim)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("get item image failed", err), nil
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
	outfitID, err := req.RequireString("outfit_id")
	if err != nil {
		return mcp.NewToolResultError("outfit_id is required"), nil
	}
	variant := s.variantOrDefault(req)

	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits/"+url.PathEscape(outfitID), nil, nil)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("get outfit failed", err), nil
	}

	garments, err := extractOutfitGarments(raw)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("could not read outfit garments", err), nil
	}
	if len(garments) == 0 {
		return mcp.NewToolResultError("outfit has no garments with images"), nil
	}

	content := []mcp.Content{}
	manifest := make([]map[string]any, 0, len(garments))

	for _, g := range garments {
		itemID := stringFromJSON(g, "id")
		img, err := s.client.ItemImageFromPayload(ctx, itemID, g, variant, s.cfg.ImageMaxDim)
		entry := map[string]any{"item_id": itemID}
		if err != nil {
			entry["error"] = err.Error()
			manifest = append(manifest, entry)
			s.log.Warn("outfit image fetch failed", "item_id", itemID, "err", err)
			continue
		}
		entry["mime_type"] = img.MIME
		entry["bytes"] = len(img.Data)
		manifest = append(manifest, entry)
		content = append(content, mcp.NewImageContent(base64.StdEncoding.EncodeToString(img.Data), img.MIME))
	}

	header, _ := json.Marshal(map[string]any{
		"outfit_id": outfitID, "variant": variant, "garments": manifest,
	})
	// manifest text first, then the images
	return &mcp.CallToolResult{
		Content: append([]mcp.Content{mcp.NewTextContent(string(header))}, content...),
	}, nil
}

func (s *Server) variantOrDefault(req mcp.CallToolRequest) string {
	v := req.GetString("variant", "")
	if v == "" {
		return string(s.cfg.ImageVariant)
	}
	return v
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
	return nil, fmt.Errorf("no garment list found in outfit payload")
}

func stringFromJSON(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
