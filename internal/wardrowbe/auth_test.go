package wardrowbe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startOIDCCapturing serves discovery + a /token endpoint whose handler receives
// the parsed request, so tests can assert which refresh_token was posted and
// return a dynamic body.
func startOIDCCapturing(t *testing.T, tokenFn func(r *http.Request) (int, string)) (*OIDCTokenProvider, func()) {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"token_endpoint":"`+srv.URL+`/token"}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code, body := tokenFn(r)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	})
	srv = httptest.NewTLSServer(mux)
	p := &OIDCTokenProvider{Issuer: srv.URL, ClientID: "client-1", HTTPClient: srv.Client()}
	return p, srv.Close
}

// TestOIDCRefreshTokenFileLoadedOnStartup: the persisted token (a previous
// rotation) is used over the now-dead configured seed.
func TestOIDCRefreshTokenFileLoadedOnStartup(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rt")
	if err := os.WriteFile(file, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sentRT string
	p, stop := startOIDCCapturing(t, func(r *http.Request) (int, string) {
		sentRT = r.PostFormValue("refresh_token")
		return 200, `{"id_token":"` + makeIDToken(map[string]any{"sub": "u1"}) + `"}`
	})
	defer stop()
	p.RefreshToken = "seed-token" // must lose to the file
	p.RefreshTokenFile = file

	if _, err := p.SyncPayload(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sentRT != "file-token" {
		t.Errorf("posted refresh_token = %q, want the file token to win over the seed", sentRT)
	}
}

// TestOIDCRefreshTokenFilePersistedOnRotation: a rotated refresh token is written
// back to the file so a restart resumes from it.
func TestOIDCRefreshTokenFilePersistedOnRotation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rt")
	p, stop := startOIDCCapturing(t, func(r *http.Request) (int, string) {
		return 200, `{"id_token":"` + makeIDToken(map[string]any{"sub": "u1"}) + `","refresh_token":"rotated-1"}`
	})
	defer stop()
	p.RefreshToken = "seed-token"
	p.RefreshTokenFile = file

	if _, err := p.SyncPayload(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read persisted token: %v", err)
	}
	if strings.TrimSpace(string(data)) != "rotated-1" {
		t.Errorf("persisted token = %q, want rotated-1", strings.TrimSpace(string(data)))
	}
}

// TestOIDCRefreshTokenFileOnlyBootstrap: a file-only config (no seed) must route
// into the refresh grant and use the pre-populated file token. This guards the
// documented bootstrap path — writing the initial token to the file — which the
// static-vs-refresh dispatch would otherwise mistake for "no token configured".
func TestOIDCRefreshTokenFileOnlyBootstrap(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rt")
	if err := os.WriteFile(file, []byte("bootstrap-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sentRT string
	p, stop := startOIDCCapturing(t, func(r *http.Request) (int, string) {
		sentRT = r.PostFormValue("refresh_token")
		return 200, `{"id_token":"` + makeIDToken(map[string]any{"sub": "u1"}) + `"}`
	})
	defer stop()
	p.RefreshTokenFile = file // no RefreshToken seed

	if _, err := p.SyncPayload(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sentRT != "bootstrap-token" {
		t.Errorf("posted refresh_token = %q, want the file token used as the bootstrap", sentRT)
	}
}

// TestOIDCRefreshTokenFileEmptyNoSeed: file-only with an absent file and no seed
// fails fast with an actionable error rather than POSTing an empty refresh_token.
func TestOIDCRefreshTokenFileEmptyNoSeed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "absent-rt")
	tokenCalled := false
	p, stop := startOIDCCapturing(t, func(r *http.Request) (int, string) {
		tokenCalled = true
		return 200, `{}`
	})
	defer stop()
	p.RefreshTokenFile = file // no seed, file does not exist

	_, err := p.SyncPayload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refresh token file") {
		t.Fatalf("err = %v, want an actionable empty-file error", err)
	}
	if tokenCalled {
		t.Error("token endpoint must not be called with an empty refresh_token")
	}
}

// makeIDToken builds an unsigned-payload JWT carrying the given claims. The
// provider decodes the payload without verifying the signature (the token is
// fetched over TLS straight from the issuer), so the signature is a placeholder.
func makeIDToken(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none", "typ": "JWT"}) + "." + enc(claims) + ".sig"
}

// startOIDC spins up an https stub serving the discovery document and the token
// endpoint, and returns a provider wired to trust it. `disco` receives the live
// server base URL so it can advertise an absolute, same-host token_endpoint.
func startOIDC(t *testing.T, disco func(self string) (int, string), token func() (int, string)) (*OIDCTokenProvider, func()) {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		code, body := disco(srv.URL)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		code, body := token()
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	})
	srv = httptest.NewTLSServer(mux)

	p := &OIDCTokenProvider{
		Issuer:       srv.URL,
		ClientID:     "client-1",
		RefreshToken: "refresh-1",
		HTTPClient:   srv.Client(),
	}
	return p, srv.Close
}

func selfTokenEndpoint(self string) (int, string) {
	return 200, `{"token_endpoint":"` + self + `/token"}`
}

func TestOIDCSyncPayloadHappyPath(t *testing.T) {
	idTok := makeIDToken(map[string]any{"sub": "user-123", "email": "u@example.com", "name": "User Name"})
	p, stop := startOIDC(t,
		selfTokenEndpoint,
		func() (int, string) { return 200, `{"id_token":"` + idTok + `"}` },
	)
	defer stop()

	got, err := p.SyncPayload(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ExternalID != "user-123" || got.Email != "u@example.com" || got.DisplayName != "User Name" {
		t.Errorf("payload = %#v", got)
	}
	if got.IDToken != idTok {
		t.Errorf("IDToken = %q, want the raw refreshed id_token to be forwarded", got.IDToken)
	}
}

// TestOIDCStaticIDToken covers the optional-refresh path: with no refresh token
// configured, the provider uses the static id_token directly and never contacts
// the issuer's token endpoint.
func TestOIDCStaticIDToken(t *testing.T) {
	idTok := makeIDToken(map[string]any{"sub": "user-123", "email": "u@example.com", "name": "User Name"})
	p := &OIDCTokenProvider{
		Issuer:   "https://issuer.invalid", // must never be contacted
		ClientID: "client-1",
		IDToken:  idTok,
	}

	got, err := p.SyncPayload(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ExternalID != "user-123" || got.Email != "u@example.com" || got.DisplayName != "User Name" {
		t.Errorf("payload = %#v", got)
	}
	if got.IDToken != idTok {
		t.Errorf("IDToken = %q, want the static id_token forwarded", got.IDToken)
	}
}

// TestOIDCNoTokenSource guards the provider-level invariant that config
// validation also enforces: with neither a refresh token nor a static id_token,
// SyncPayload fails rather than sending an empty token.
func TestOIDCNoTokenSource(t *testing.T) {
	p := &OIDCTokenProvider{Issuer: "https://issuer.invalid", ClientID: "client-1"}
	if _, err := p.SyncPayload(context.Background()); err == nil {
		t.Fatal("expected error: no refresh token or id_token configured")
	}
}

// TestOIDCDisplayNameFallback covers issuers that omit the `name` claim (e.g.
// Cloudflare Access refresh-grant id_tokens carry only `sub`). The backend
// requires a non-empty display_name, so the provider must fall back to email,
// then sub, rather than send an empty string.
func TestOIDCDisplayNameFallback(t *testing.T) {
	cases := []struct {
		name        string
		claims      map[string]any
		wantDisplay string
	}{
		{"name present", map[string]any{"sub": "u1", "email": "u@example.com", "name": "User One"}, "User One"},
		{"name missing falls back to email", map[string]any{"sub": "u1", "email": "u@example.com"}, "u@example.com"},
		{"name and email missing falls back to sub", map[string]any{"sub": "u1"}, "u1"},
		{"blank name falls back to email", map[string]any{"sub": "u1", "email": "u@example.com", "name": "   "}, "u@example.com"},
		{"blank name and email fall back to sub", map[string]any{"sub": "u1", "email": "  ", "name": "\t"}, "u1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &OIDCTokenProvider{Issuer: "https://issuer.invalid", ClientID: "c", IDToken: makeIDToken(tc.claims)}
			got, err := p.SyncPayload(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.DisplayName != tc.wantDisplay {
				t.Errorf("DisplayName = %q, want %q", got.DisplayName, tc.wantDisplay)
			}
			if got.DisplayName == "" {
				t.Error("DisplayName must never be empty (backend requires >= 1 char)")
			}
		})
	}
}

// TestOIDCStaticIDTokenExpired fails fast on an already-expired static token
// instead of forwarding it (which would re-sync on every request with no
// backoff once the backend rejects it).
func TestOIDCStaticIDTokenExpired(t *testing.T) {
	idTok := makeIDToken(map[string]any{
		"sub": "user-123",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	p := &OIDCTokenProvider{Issuer: "https://issuer.invalid", ClientID: "client-1", IDToken: idTok}

	if _, err := p.SyncPayload(context.Background()); err == nil {
		t.Fatal("expected error: static id_token expired")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error = %v, want it to mention the token expired", err)
	}
}

// TestOIDCRefreshTokenTakesPrecedence pins the documented precedence: when both
// a refresh token and a static id_token are configured, the refresh_token grant
// runs and its freshly-minted token wins (the static one is unused).
func TestOIDCRefreshTokenTakesPrecedence(t *testing.T) {
	refreshed := makeIDToken(map[string]any{"sub": "from-refresh"})
	p, stop := startOIDC(t,
		selfTokenEndpoint,
		func() (int, string) { return 200, `{"id_token":"` + refreshed + `"}` },
	)
	defer stop()
	p.IDToken = makeIDToken(map[string]any{"sub": "from-static"}) // must be ignored

	got, err := p.SyncPayload(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ExternalID != "from-refresh" || got.IDToken != refreshed {
		t.Errorf("payload = %#v, want the refreshed token to win over the static one", got)
	}
}

func TestOIDCDiscoveryCachedAcrossCalls(t *testing.T) {
	idTok := makeIDToken(map[string]any{"sub": "user-123"})
	discoveries := 0
	p, stop := startOIDC(t,
		func(self string) (int, string) {
			discoveries++
			return selfTokenEndpoint(self)
		},
		func() (int, string) { return 200, `{"id_token":"` + idTok + `"}` },
	)
	defer stop()

	for i := 0; i < 3; i++ {
		if _, err := p.SyncPayload(context.Background()); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}
	if discoveries != 1 {
		t.Errorf("discovery fetched %d times, want 1 (should be cached)", discoveries)
	}
}

func TestOIDCDiscoveryNon200(t *testing.T) {
	p, stop := startOIDC(t,
		func(string) (int, string) { return 500, `{}` },
		func() (int, string) { return 200, `{}` },
	)
	defer stop()
	if _, err := p.SyncPayload(context.Background()); err == nil {
		t.Fatal("expected error on discovery 500")
	}
}

// TestOIDCAcceptsCrossHostTokenEndpoint: major IdPs serve the token endpoint
// from a different host than the issuer (Google: accounts.google.com vs
// oauth2.googleapis.com; AWS Cognito likewise), so a cross-host https
// token_endpoint from the (TLS-authenticated) discovery document is accepted.
func TestOIDCAcceptsCrossHostTokenEndpoint(t *testing.T) {
	idTok := makeIDToken(map[string]any{"sub": "user-123"})
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		// Advertise the token endpoint on a hostname (example.com, covered by
		// the httptest certificate's SANs) that differs from the issuer host
		// (127.0.0.1). The client's dialer below routes it back to the stub.
		_, _ = io.WriteString(w, `{"token_endpoint":"https://example.com/token"}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id_token":"`+idTok+`"}`)
	})
	srv = httptest.NewTLSServer(mux)
	defer srv.Close()

	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}

	p := &OIDCTokenProvider{
		Issuer:       srv.URL, // https://127.0.0.1:<port> — host differs from example.com
		ClientID:     "client-1",
		RefreshToken: "refresh-1",
		HTTPClient:   &http.Client{Transport: tr},
	}
	if _, err := p.SyncPayload(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestOIDCTokenEndpointOverrideSkipsDiscovery: an explicit TokenEndpoint must
// be used directly, without fetching the discovery document.
func TestOIDCTokenEndpointOverrideSkipsDiscovery(t *testing.T) {
	idTok := makeIDToken(map[string]any{"sub": "user-123"})
	discoveries := 0
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		discoveries++
		_, _ = io.WriteString(w, `{"token_endpoint":"`+srv.URL+`/token"}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id_token":"`+idTok+`"}`)
	})
	srv = httptest.NewTLSServer(mux)
	defer srv.Close()

	p := &OIDCTokenProvider{
		Issuer:        srv.URL,
		ClientID:      "client-1",
		RefreshToken:  "refresh-1",
		TokenEndpoint: srv.URL + "/token",
		HTTPClient:    srv.Client(),
	}
	if _, err := p.SyncPayload(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if discoveries != 0 {
		t.Errorf("discovery fetched %d times, want 0 (explicit endpoint)", discoveries)
	}
}

// TestOIDCRotatedRefreshTokenUsedOnNextGrant: when the IdP rotates the refresh
// token, the next grant must POST the new token, not the original.
func TestOIDCRotatedRefreshTokenUsedOnNextGrant(t *testing.T) {
	idTok := makeIDToken(map[string]any{"sub": "user-123"})
	var seen []string
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"token_endpoint":"`+srv.URL+`/token"}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		seen = append(seen, r.PostFormValue("refresh_token"))
		_, _ = io.WriteString(w, `{"id_token":"`+idTok+`","refresh_token":"rotated-`+strconv.Itoa(len(seen))+`"}`)
	})
	srv = httptest.NewTLSServer(mux)
	defer srv.Close()

	p := &OIDCTokenProvider{
		Issuer:       srv.URL,
		ClientID:     "client-1",
		RefreshToken: "refresh-original",
		HTTPClient:   srv.Client(),
	}
	for i := 0; i < 2; i++ {
		if _, err := p.SyncPayload(context.Background()); err != nil {
			t.Fatalf("grant %d: unexpected error: %v", i, err)
		}
	}
	want := []string{"refresh-original", "rotated-1"}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Errorf("refresh tokens sent = %v, want %v", seen, want)
	}
}

// TestOIDCErrorBodySurfacedOnNon200: RFC 6749 §5.2 errors arrive as a 400 with
// a JSON body; the error code must reach the caller, not just the status.
func TestOIDCErrorBodySurfacedOnNon200(t *testing.T) {
	p, stop := startOIDC(t,
		selfTokenEndpoint,
		func() (int, string) {
			return 400, `{"error":"invalid_grant","error_description":"refresh token expired"}`
		},
	)
	defer stop()

	_, err := p.SyncPayload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %v, want invalid_grant surfaced from 400 body", err)
	}
}

func TestOIDCRejectsNonHTTPSTokenEndpoint(t *testing.T) {
	p, stop := startOIDC(t,
		func(self string) (int, string) {
			httpSelf := strings.Replace(self, "https://", "http://", 1)
			return 200, `{"token_endpoint":"` + httpSelf + `/token"}`
		},
		func() (int, string) { return 200, `{"id_token":"x.y.z"}` },
	)
	defer stop()

	_, err := p.SyncPayload(context.Background())
	if err == nil {
		t.Fatal("expected error: token_endpoint must be https")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error = %v, want https rejection", err)
	}
}

func TestOIDCTokenErrorField(t *testing.T) {
	p, stop := startOIDC(t,
		selfTokenEndpoint,
		func() (int, string) { return 200, `{"error":"invalid_grant","error_description":"bad refresh token"}` },
	)
	defer stop()

	_, err := p.SyncPayload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %v, want invalid_grant", err)
	}
}

func TestOIDCMissingIDToken(t *testing.T) {
	p, stop := startOIDC(t,
		selfTokenEndpoint,
		func() (int, string) { return 200, `{}` },
	)
	defer stop()
	if _, err := p.SyncPayload(context.Background()); err == nil {
		t.Fatal("expected error: token response missing id_token")
	}
}

func TestOIDCMalformedIDToken(t *testing.T) {
	p, stop := startOIDC(t,
		selfTokenEndpoint,
		func() (int, string) { return 200, `{"id_token":"not-a-valid-jwt"}` },
	)
	defer stop()
	if _, err := p.SyncPayload(context.Background()); err == nil {
		t.Fatal("expected error: malformed id_token")
	}
}

func TestOIDCMissingSubClaim(t *testing.T) {
	idTok := makeIDToken(map[string]any{"email": "u@example.com"})
	p, stop := startOIDC(t,
		selfTokenEndpoint,
		func() (int, string) { return 200, `{"id_token":"` + idTok + `"}` },
	)
	defer stop()

	_, err := p.SyncPayload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sub") {
		t.Errorf("err = %v, want missing-sub rejection", err)
	}
}
