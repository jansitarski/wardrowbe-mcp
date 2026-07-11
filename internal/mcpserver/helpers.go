package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
	"github.com/mark3labs/mcp-go/mcp"
)

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func itoa(v int) string { return strconv.Itoa(v) }

// isValidDate reports whether s is a canonical calendar date in YYYY-MM-DD form
// (time.Parse alone accepts non-canonical inputs like "2026-6-1"). Validating at
// the gateway turns a vague backend 422 into a clear tool-level message.
func isValidDate(s string) bool {
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Format("2006-01-02") == s
}

// requireID returns the named argument as a trimmed, non-empty string, or a
// tool error result. RequireString alone accepts "" — and an empty path segment
// turns e.g. GET /items/{id} into GET /items/, which the backend redirects to
// the whole collection, silently returning the wrong data.
func requireID(req mcp.CallToolRequest, key string) (string, *mcp.CallToolResult) {
	v, err := req.RequireString(key)
	if err != nil || strings.TrimSpace(v) == "" {
		return "", mcp.NewToolResultError(key + " is required and must be a non-empty string")
	}
	return strings.TrimSpace(v), nil
}

// argBool reads an optional boolean argument strictly: a present but non-bool
// value (e.g. favorite:"yes") is a tool error, not a silent default — this is
// often a write path and the default would be persisted.
func argBool(req mcp.CallToolRequest, key string) (val bool, present bool, errRes *mcp.CallToolResult) {
	raw, ok := req.GetArguments()[key]
	if !ok {
		return false, false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, true, mcp.NewToolResultErrorf("%s must be a boolean (true/false)", key)
	}
	return b, true, nil
}

// argIntDefault reads an optional integer argument strictly (see argInt),
// applying def when absent and clamping the result to [lo, hi].
func argIntDefault(req mcp.CallToolRequest, key string, def, lo, hi int) (int, *mcp.CallToolResult) {
	v, present, errRes := argInt(req, key)
	if errRes != nil {
		return 0, errRes
	}
	if !present {
		v = def
	}
	return clampInt(v, lo, hi), nil
}

// argInt reads an optional integer argument strictly, rejecting present but
// uncoercible values (e.g. rating:"high") instead of silently defaulting.
func argInt(req mcp.CallToolRequest, key string) (val int, present bool, errRes *mcp.CallToolResult) {
	raw, ok := req.GetArguments()[key]
	if !ok {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, true, mcp.NewToolResultErrorf("%s must be an integer", key)
		}
		return int(v), true, nil
	case int:
		return v, true, nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, true, mcp.NewToolResultErrorf("%s must be an integer", key)
		}
		return int(n), true, nil
	default:
		return 0, true, mcp.NewToolResultErrorf("%s must be an integer", key)
	}
}

// argFloat reads an optional numeric argument strictly, rejecting present but
// uncoercible values (e.g. latitude:"north") instead of silently defaulting.
func argFloat(req mcp.CallToolRequest, key string) (val float64, present bool, errRes *mcp.CallToolResult) {
	raw, ok := req.GetArguments()[key]
	if !ok {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case float64:
		return v, true, nil
	case int:
		return float64(v), true, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, true, mcp.NewToolResultErrorf("%s must be a number", key)
		}
		return f, true, nil
	default:
		return 0, true, mcp.NewToolResultErrorf("%s must be a number", key)
	}
}

// safeErrText renders an error for return to the MCP caller without leaking
// backend internals. An *APIError already reports only method/path/status; any
// other error (network, TLS, DNS — which embed internal hostnames/IPs) is
// reduced to a generic message. Log the full error server-side separately.
func safeErrText(err error) string {
	var apiErr *wardrowbe.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Error()
	}
	return "request failed"
}

// toolErr returns a tool error result for a backend-call failure with its
// underlying error sanitized via safeErrText, so transport internals never reach
// the MCP caller even if a future code path forgets to sanitize at the client
// layer. Use this for errors that originate from the backend client; locally
// constructed, already-safe messages (e.g. image-fetch validation) can be
// surfaced verbatim.
func toolErr(summary string, err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(summary + ": " + safeErrText(err))
}

// firstOutfitID fetches the id of the latest outfit, or an error if there are
// none. page_size=1 keeps the backend from serializing a whole page of outfits
// just to read one id (the backend paginates with page_size, not limit).
func (s *Server) firstOutfitID(ctx context.Context) (string, error) {
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits", url.Values{"page_size": {"1"}}, nil)
	if err != nil {
		return "", err
	}
	list, err := wardrowbe.CoerceList(raw)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("no outfits found")
	}
	var first struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(list[0], &first); err != nil {
		return "", fmt.Errorf("decode latest outfit: %w", err)
	}
	if first.ID == "" {
		return "", fmt.Errorf("latest outfit has no id")
	}
	return first.ID, nil
}

// validOccasions / validTimesOfDay mirror the upstream server.py enums.
var validOccasions = map[string]struct{}{
	"beach": {}, "brunch": {}, "business-casual": {}, "casual": {}, "date": {},
	"dinner": {}, "formal": {}, "gym": {}, "hiking": {}, "interview": {},
	"lounge": {}, "office": {}, "outdoor": {}, "party": {}, "running": {},
	"smart-casual": {}, "sport": {}, "sporty": {}, "travel": {}, "wedding": {},
	"weekend": {}, "work": {},
}

var validTimesOfDay = map[string]struct{}{
	"morning": {}, "afternoon": {}, "evening": {}, "night": {}, "full day": {},
}

// validSeasons / validFormalities mirror the backend's canonical item-tag
// vocabulary. The authoring endpoints accept free-form strings, but the tool
// layer pins the canonical values so authored outfits stay filterable.
var validSeasons = map[string]struct{}{
	"spring": {}, "summer": {}, "fall": {}, "winter": {}, "all-season": {},
}

var validFormalities = map[string]struct{}{
	"very-casual": {}, "casual": {}, "smart-casual": {}, "business-casual": {},
	"formal": {}, "very-formal": {},
}

func occasionList() []string  { return sortedKeys(validOccasions) }
func timeOfDayList() []string { return sortedKeys(validTimesOfDay) }
func seasonList() []string    { return sortedKeys(validSeasons) }
func formalityList() []string { return sortedKeys(validFormalities) }

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out) // stable order for the enum schema
	return out
}
