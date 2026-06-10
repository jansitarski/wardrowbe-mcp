package wardrowbe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

// TestOIDCRejectsTokenEndpointOnDifferentHost is the key security test: a
// tampered/MITM'd discovery document pointing the token endpoint at an attacker
// host (where the client secret + refresh token would be POSTed) must be refused.
func TestOIDCRejectsTokenEndpointOnDifferentHost(t *testing.T) {
	p, stop := startOIDC(t,
		func(string) (int, string) {
			return 200, `{"token_endpoint":"https://attacker.example.com/token"}`
		},
		func() (int, string) { return 200, `{"id_token":"x.y.z"}` }, // must never be reached
	)
	defer stop()

	_, err := p.SyncPayload(context.Background())
	if err == nil {
		t.Fatal("expected error: token_endpoint host must match issuer")
	}
	if !strings.Contains(err.Error(), "does not match issuer host") {
		t.Errorf("error = %v, want host-mismatch rejection", err)
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
