package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOIDCClaims(t *testing.T) {
	claims := OIDCClaims{Subject: "subject", Email: "alice@example.com", EmailVerified: true, Name: "Alice"}
	if claims.Username() != "alice" || !claims.Admitted(nil, []string{"EXAMPLE.COM"}) || claims.GitAuthor() != "Alice <alice@example.com>" {
		t.Fatalf("unexpected claims behaviour: %+v", claims)
	}
	if (OIDCClaims{Email: "alice@example.com"}).Admitted([]string{"subject"}, []string{"example.com"}) {
		t.Fatal("unverified email was admitted")
	}
}

func TestNewOIDCDiscovery(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/auth",
			"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer server.Close()

	client, err := NewOIDC(context.Background(), OIDCOptions{Issuer: server.URL, ClientID: "hmd", BaseURL: "https://wiki.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if location := client.AuthCodeURL("state", "verifier"); !strings.HasPrefix(location, server.URL+"/auth?") || !strings.Contains(location, "code_challenge=") {
		t.Fatalf("unexpected authorisation URL %q", location)
	}
}

func TestValidateIconSVG(t *testing.T) {
	if err := ValidateIconSVG([]byte(`<svg viewBox="0 0 24 24"></svg>`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIconSVG([]byte(`<svg viewBox="0 0 24 16"></svg>`)); err == nil {
		t.Fatal("non-square SVG accepted")
	}
}
