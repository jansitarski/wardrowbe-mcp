package mcpserver

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/server"
)

const mcpPath = "/mcp"

// HTTPHandler builds the full HTTP handler: an anonymous readiness probe at "/",
// and the bearer-gated Streamable HTTP MCP endpoint at "/mcp".
func (s *Server) HTTPHandler() http.Handler {
	streamable := server.NewStreamableHTTPServer(
		s.mcp,
		server.WithEndpointPath(mcpPath),
	)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, s.bearerGate(streamable))
	mux.HandleFunc("/", s.rootHandler)
	return mux
}

// rootHandler answers readiness/liveness probes anonymously. Any non-/mcp path
// that is not exactly "/" is a 404.
func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","service":"wardrowbe-mcp"}`))
}

// bearerGate enforces the static MCP_API_KEY on the MCP endpoint and emits an
// RFC 9728 WWW-Authenticate header on 401 so connectors can discover OAuth.
func (s *Server) bearerGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			s.writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	// constant-time compare to avoid leaking the key via timing.
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIKey)) == 1
}

func (s *Server) writeUnauthorized(w http.ResponseWriter) {
	if s.cfg.PortalResourceURL != "" {
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer resource_metadata="%s"`, s.cfg.PortalResourceURL))
	} else {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
