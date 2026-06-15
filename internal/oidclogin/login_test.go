package oidclogin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewPKCEChallengeMatchesVerifier(t *testing.T) {
	v, c, err := newPKCE()
	if err != nil {
		t.Fatalf("newPKCE: %v", err)
	}
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if c != want {
		t.Errorf("challenge %q is not S256(verifier)", c)
	}
	if strings.ContainsAny(c, "+/=") {
		t.Errorf("challenge must be base64url without padding: %q", c)
	}
}

func TestBuildAuthURL(t *testing.T) {
	got := buildAuthURL("https://idp.example/authorize", "client-1",
		"http://127.0.0.1:8976/callback", []string{"openid", "offline_access"}, "st-1", "chal-1")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "client-1",
		"redirect_uri":          "http://127.0.0.1:8976/callback",
		"scope":                 "openid offline_access",
		"state":                 "st-1",
		"code_challenge":        "chal-1",
		"code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestParseLoopbackRedirect(t *testing.T) {
	ok := []string{"http://127.0.0.1:8976/callback", "http://localhost:9000/cb", "http://[::1]:8080/x"}
	for _, r := range ok {
		if _, err := parseLoopbackRedirect(r); err != nil {
			t.Errorf("expected %q to be accepted: %v", r, err)
		}
	}
	bad := []string{"https://127.0.0.1/cb", "http://example.com/cb", "http://8.8.8.8/cb", "not-a-url", ""}
	for _, r := range bad {
		if _, err := parseLoopbackRedirect(r); err == nil {
			t.Errorf("expected %q to be rejected", r)
		}
	}
}

func TestResolveEndpointsDiscovery(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_, _ = io.WriteString(w, `{"authorization_endpoint":"https://idp/authorize","token_endpoint":"https://idp/token"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	a, tk, err := resolveEndpoints(context.Background(), srv.Client(), Options{Issuer: srv.URL})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if a != "https://idp/authorize" || tk != "https://idp/token" {
		t.Errorf("endpoints = %q, %q", a, tk)
	}
}

func TestResolveEndpointsOverridesSkipDiscovery(t *testing.T) {
	// Issuer is bogus; overrides must be used without any network call.
	a, tk, err := resolveEndpoints(context.Background(), &http.Client{Timeout: time.Second}, Options{
		Issuer:        "https://unreachable.invalid",
		AuthEndpoint:  "https://idp/authorize",
		TokenEndpoint: "https://idp/token",
	})
	if err != nil || a != "https://idp/authorize" || tk != "https://idp/token" {
		t.Fatalf("got %q, %q, err=%v", a, tk, err)
	}
}

func TestResolveEndpointsRejectsNonHTTPS(t *testing.T) {
	_, _, err := resolveEndpoints(context.Background(), nil, Options{
		AuthEndpoint:  "http://idp/authorize",
		TokenEndpoint: "https://idp/token",
	})
	if err == nil {
		t.Fatal("expected error for non-https authorization endpoint")
	}
}

func TestCallbackHandler(t *testing.T) {
	const path, state = "/callback", "st-1"

	do := func(target string) callbackOutcome {
		ch := make(chan callbackOutcome, 1)
		h := callbackHandler(path, state, ch)
		req := httptest.NewRequest(http.MethodGet, target, nil)
		h(httptest.NewRecorder(), req)
		select {
		case o := <-ch:
			return o
		default:
			return callbackOutcome{err: errNoSignal}
		}
	}

	if o := do("/callback?code=abc&state=st-1"); o.err != nil || o.code != "abc" {
		t.Errorf("happy path: code=%q err=%v", o.code, o.err)
	}
	if o := do("/callback?code=abc&state=WRONG"); o.err == nil {
		t.Error("state mismatch should error")
	}
	if o := do("/callback?error=access_denied&state=st-1"); o.err == nil {
		t.Error("error param should error")
	}
	if o := do("/callback?state=st-1"); o.err == nil {
		t.Error("missing code should error")
	}
	// Wrong path: handler 404s and must NOT signal.
	if o := do("/other?code=abc&state=st-1"); o.err != errNoSignal {
		t.Error("wrong path should not produce an outcome")
	}
}

var errNoSignal = &noSignalErr{}

type noSignalErr struct{}

func (*noSignalErr) Error() string { return "no signal" }

func TestExchangeCodeSendsSecretAndVerifier(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		_, _ = io.WriteString(w, `{"refresh_token":"rt-1","id_token":"`+fakeIDToken+`","access_token":"at-1","expires_in":300}`)
	}))
	defer srv.Close()

	res, err := exchangeCode(context.Background(), srv.Client(), srv.URL, Options{
		ClientID: "client-1", ClientSecret: "secret-1",
	}, "code-1", "verifier-1", "http://127.0.0.1:8976/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.RefreshToken != "rt-1" || res.AccessToken != "at-1" || res.ExpiresIn != 300 {
		t.Errorf("result = %#v", res)
	}
	if res.Subject != "user-123" || res.Email != "u@example.com" {
		t.Errorf("claims not decoded: sub=%q email=%q", res.Subject, res.Email)
	}
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("code") != "code-1" ||
		gotForm.Get("code_verifier") != "verifier-1" || gotForm.Get("client_secret") != "secret-1" ||
		gotForm.Get("client_id") != "client-1" {
		t.Errorf("token request form = %v", gotForm)
	}
}

func TestExchangeCodeSurfacesError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"bad code"}`)
	}))
	defer srv.Close()
	_, err := exchangeCode(context.Background(), srv.Client(), srv.URL, Options{ClientID: "c"}, "x", "y", "http://127.0.0.1:8976/callback")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v, want invalid_grant surfaced", err)
	}
}

// TestRunFullLoopback exercises the whole flow end-to-end with pinned endpoints
// (no discovery, no browser): a goroutine runs Run; the test plays the IdP by
// GETting the captured authorize URL's redirect with a code + the real state.
func TestRunFullLoopback(t *testing.T) {
	tokenSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code") != "code-xyz" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		_, _ = io.WriteString(w, `{"refresh_token":"rt-final","id_token":"`+fakeIDToken+`"}`)
	}))
	defer tokenSrv.Close()

	// A free loopback port for the redirect listener.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	redirect := "http://" + addr + "/callback"

	out := &syncBuffer{}
	resCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		res, err := Run(ctx, Options{
			ClientID:      "client-1",
			AuthEndpoint:  "https://idp.example/authorize",
			TokenEndpoint: tokenSrv.URL,
			RedirectURL:   redirect,
			Scopes:        []string{"openid", "offline_access"},
			HTTPClient:    tokenSrv.Client(),
		}, out)
		if err != nil {
			errCh <- err
			return
		}
		resCh <- res
	}()

	state := waitForState(t, out)
	// Retry until the loopback listener is accepting, then deliver the code.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, gerr := http.Get(redirect + "?code=code-xyz&state=" + url.QueryEscape(state))
		if gerr == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("callback never became reachable: %v", gerr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case res := <-resCh:
		if res.RefreshToken != "rt-final" {
			t.Errorf("RefreshToken = %q, want rt-final", res.RefreshToken)
		}
	case err := <-errCh:
		t.Fatalf("Run returned error: %v", err)
	case <-ctx.Done():
		t.Fatal("Run did not complete")
	}
}

// fakeIDToken is an unsigned JWT carrying sub/email (payload-only; the decoder
// does not verify signatures).
var fakeIDToken = func() string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(`{"sub":"user-123","email":"u@example.com"}`) + ".sig"
}()

func waitForState(t *testing.T, out *syncBuffer) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, field := range strings.Fields(out.String()) {
			if u, err := url.Parse(strings.TrimSpace(field)); err == nil {
				if s := u.Query().Get("state"); s != "" {
					return s
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("authorize URL with state never printed")
	return ""
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
