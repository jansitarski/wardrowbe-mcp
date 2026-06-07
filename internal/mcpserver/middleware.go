package mcpserver

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

const mcpPath = "/mcp"

// readinessTimeout bounds the backend ping behind /readyz.
const readinessTimeout = 3 * time.Second

// HTTPHandler builds the full HTTP handler. Endpoints:
//   - GET  /        liveness (static; process is up)
//   - GET  /readyz  readiness (bounded backend ping)
//   - POST /mcp     bearer-gated Streamable HTTP MCP endpoint
//
// The /mcp handler is wrapped with an inbound body-size cap and a concurrency
// limiter; the whole mux is wrapped with panic recovery so a panic in any layer
// returns a clean 500 instead of a dropped connection.
func (s *Server) HTTPHandler() http.Handler {
	streamable := server.NewStreamableHTTPServer(
		s.mcp,
		server.WithEndpointPath(mcpPath),
	)

	mcpHandler := s.bearerGate(streamable)
	mcpHandler = http.MaxBytesHandler(mcpHandler, s.cfg.MaxBodyBytes)
	mcpHandler = s.limitConcurrency(mcpHandler)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, mcpHandler)
	mux.HandleFunc("/readyz", s.readyHandler)
	mux.HandleFunc("/", s.rootHandler)
	return s.recoverPanic(mux)
}

// rootHandler answers liveness probes anonymously: it reports only that the
// process is up, with no backend dependency (so a backend blip never makes an
// orchestrator kill the pod). Any non-"/" path here is a 404.
func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, `{"status":"ok","service":"wardrowbe-mcp"}`)
}

// readyHandler answers readiness probes by pinging the backend within a short
// deadline. 200 means the backend is reachable; 503 means it is not.
func (s *Server) readyHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := s.client.Ping(ctx); err != nil {
		s.log.Warn("readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, `{"status":"unavailable"}`)
		return
	}
	writeJSON(w, http.StatusOK, `{"status":"ready"}`)
}

// limitConcurrency caps in-flight /mcp requests via a buffered semaphore,
// returning 503 immediately when the server is saturated rather than queuing
// unboundedly (which, with slow backend calls, would pile up to OOM).
func (s *Server) limitConcurrency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
			next.ServeHTTP(w, r)
		default:
			s.log.Warn("at capacity, rejecting request", "limit", cap(s.sem))
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusServiceUnavailable, `{"error":"server at capacity"}`)
		}
	})
}

// recoverPanic turns a panic in any handler/middleware into a logged 500 rather
// than a silently dropped connection.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in http handler", "panic", rec, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, `{"error":"internal"}`)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
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
