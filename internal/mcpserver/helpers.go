package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
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

// isValidDate reports whether s is a calendar date in YYYY-MM-DD form. Validating
// at the gateway turns a vague backend 422 into a clear tool-level message.
func isValidDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
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

// firstOutfitID fetches the outfit list and returns the id of the first
// (latest) outfit, or an error if there are none.
func (s *Server) firstOutfitID(ctx context.Context) (string, error) {
	raw, err := s.client.Request(ctx, http.MethodGet, "/outfits", nil, nil)
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

func occasionList() []string  { return sortedKeys(validOccasions) }
func timeOfDayList() []string { return sortedKeys(validTimesOfDay) }

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out) // stable order for the enum schema
	return out
}
