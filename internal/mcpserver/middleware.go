package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

const mcpPath = "/mcp"

// readinessTimeout bounds the backend ping behind /readyz.
const readinessTimeout = 3 * time.Second

// readyCacheTTL bounds how often /readyz actually pings the backend. Because the
// endpoint is unauthenticated, caching the result for a short window caps the
// backend load any caller can drive through it to ~1 ping/TTL, while still being
// fresh enough for a k8s readiness probe (which polls on the order of seconds).
const readyCacheTTL = 1 * time.Second

// HTTPHandler builds the full HTTP handler. Endpoints:
//   - GET  /        liveness (static; process is up)
//   - GET  /readyz  readiness (bounded backend ping)
//   - POST /mcp     bearer-gated Streamable HTTP MCP endpoint
//
// The /mcp handler is wrapped with an inbound body-size cap; the whole mux is
// wrapped with panic recovery so a panic in any layer returns a clean 500
// instead of a dropped connection.
func (s *Server) HTTPHandler() http.Handler {
	streamable := server.NewStreamableHTTPServer(
		s.mcp,
		server.WithEndpointPath(mcpPath),
	)

	mcpHandler := s.bearerGate(streamable)
	mcpHandler = http.MaxBytesHandler(mcpHandler, s.cfg.MaxBodyBytes)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, mcpHandler)
	mux.HandleFunc("/readyz", s.readyHandler)
	mux.HandleFunc("/", s.rootHandler)
	return s.recoverPanic(securityHeaders(mux))
}

// securityHeaders sets conservative defaults on every response. The surface is a
// JSON API, but a browser navigating directly to the origin would otherwise
// render/cache the JSON; nosniff + no-store close that off as defense-in-depth.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
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
	if err := s.readiness(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, `{"status":"unavailable"}`)
		return
	}
	writeJSON(w, http.StatusOK, `{"status":"ready"}`)
}

// readiness returns the backend reachability result, pinging at most once per
// readyCacheTTL and serving a cached result otherwise. The mutex is held across
// the (readinessTimeout-bounded) ping so concurrent callers wait briefly for
// the fresh result instead of each launching their own backend request. The
// ping runs on a context detached from any caller: /readyz is unauthenticated,
// and a probe that disconnects mid-ping must not get its cancellation cached
// and served to every other caller as "not ready".
func (s *Server) readiness() error {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if !s.readyChecked.IsZero() && time.Since(s.readyChecked) < readyCacheTTL {
		return s.readyErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer cancel()
	err := s.client.Ping(ctx)
	if err != nil {
		s.log.Warn("readiness check failed", "error", err)
	}
	s.readyChecked = time.Now()
	s.readyErr = err
	return err
}

// recoverPanic turns a panic in any handler/middleware into a logged 500 rather
// than a silently dropped connection. http.ErrAbortHandler is re-panicked — it
// is net/http's sentinel for deliberately aborting a response — and once a
// response has started, no second status line/body is written on top of it.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &trackingWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				s.log.Error("panic in http handler", "panic", rec, "path", r.URL.Path)
				if !tw.wrote {
					writeJSON(w, http.StatusInternalServerError, `{"error":"internal"}`)
				}
			}
		}()
		next.ServeHTTP(tw, r)
	})
}

// trackingWriter records whether the response has started, so the panic
// recovery doesn't write a second header/body mid-stream. Flush is forwarded
// for the streaming (SSE) responses the MCP transport uses.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *trackingWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackingWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *trackingWriter) Flush() {
	w.wrote = true
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
	// RFC 7235: the auth scheme is case-insensitive ("bearer x" is valid).
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	// Compare SHA-256 digests in constant time: ConstantTimeCompare alone
	// short-circuits on length mismatch, leaking the key length. The
	// configured key's digest is precomputed in New.
	gotSum := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(gotSum[:], s.apiKeyHash[:]) == 1
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
