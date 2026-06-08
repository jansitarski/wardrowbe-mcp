package mcpserver

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jansitarski/wardrowbe-mcp/internal/wardrowbe"
)

func TestIsValidDate(t *testing.T) {
	valid := []string{"2026-06-08", "2000-01-01", "2026-12-31"}
	invalid := []string{"", "yesterday", "2026-13-01", "2026-06-31", "06/08/2026", "2026-6-8", "2026-06-08T00:00:00Z"}
	for _, d := range valid {
		if !isValidDate(d) {
			t.Errorf("isValidDate(%q) = false, want true", d)
		}
	}
	for _, d := range invalid {
		if isValidDate(d) {
			t.Errorf("isValidDate(%q) = true, want false", d)
		}
	}
}

func TestSafeErrTextSanitizesNetworkErrors(t *testing.T) {
	// An APIError is already safe — keep its status/path.
	apiErr := &wardrowbe.APIError{StatusCode: 404, Method: http.MethodGet, Path: "/items/x"}
	if got := safeErrText(apiErr); !strings.Contains(got, "404") {
		t.Errorf("APIError text = %q, want it to mention 404", got)
	}

	// A transport error embeds an internal address — it must NOT leak through.
	netErr := errors.New(`Get "http://backend.internal:8000": dial tcp 10.0.0.5:8000: connect: refused`)
	got := safeErrText(netErr)
	if strings.Contains(got, "10.0.0.5") || strings.Contains(got, "backend.internal") {
		t.Errorf("safeErrText leaked internal address: %q", got)
	}
	if got != "request failed" {
		t.Errorf("safeErrText(netErr) = %q, want %q", got, "request failed")
	}
}
