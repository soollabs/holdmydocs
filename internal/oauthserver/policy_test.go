package oauthserver

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"reflect"
	"testing"
)

func TestNormaliseScopes(t *testing.T) {
	got, err := NormaliseScopes([]string{"write", "read", "read"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"read", "write"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
	if _, err := NormaliseScopes([]string{"openid"}); !errors.Is(err, errInvalidScope) {
		t.Fatalf("unknown scope error = %v", err)
	}
}

func TestEffectiveScopesDoesNotElevateSettings(t *testing.T) {
	tests := []struct {
		name                          string
		issued, consent, user, client []string
		want                          []string
	}{
		{
			name:   "all bounds include settings",
			issued: []string{"settings"}, consent: []string{"settings"},
			user: []string{"settings"}, client: []string{"settings"},
			want: []string{"read", "write", "settings"},
		},
		{
			name:   "client policy removes administrator scope",
			issued: []string{"settings"}, consent: []string{"settings"},
			user: []string{"settings"}, client: []string{"read", "write"},
			want: []string{"read", "write"},
		},
		{
			name:   "empty intersection denies",
			issued: []string{"write"}, consent: []string{"write"},
			user: []string{"read"}, client: []string{"write"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveScopes(tt.issued, tt.consent, tt.user, tt.client); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("effective scopes = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPKCES256Validation(t *testing.T) {
	verifier := "aBc123-._~aBc123-._~aBc123-._~aBc123-._~aBc123"
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if err := ValidateS256Challenge(challenge); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePKCEVerifier(verifier, challenge); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePKCEVerifier(verifier+"!", challenge); !errors.Is(err, errInvalidPKCE) {
		t.Fatalf("invalid verifier error = %v", err)
	}
	if err := ValidateS256Challenge(challenge + "="); !errors.Is(err, errInvalidPKCE) {
		t.Fatalf("padded challenge error = %v", err)
	}
}

func TestValidateResource(t *testing.T) {
	const resource = "https://wiki.example.test/_/mcp"
	for _, values := range [][]string{{resource}} {
		if err := ValidateResource(values, resource, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, values := range [][]string{
		nil,
		{resource, resource},
		{"https://wiki.example.test/_/mcp/"},
		{"https://wiki.example.test/_/mcp?x=y"},
		{"https://user@wiki.example.test/_/mcp"},
		{"//wiki.example.test/_/mcp"},
	} {
		if err := ValidateResource(values, resource, true); !errors.Is(err, errInvalidResource) {
			t.Errorf("ValidateResource(%v) = %v, want invalid resource", values, err)
		}
	}
	if err := ValidateResource(nil, resource, false); err != nil {
		t.Fatalf("optional resource: %v", err)
	}
	if err := ValidateResource([]string{resource}, resource, false); err != nil {
		t.Fatalf("inherited resource: %v", err)
	}
	if parsed, _ := url.Parse(resource); parsed.Fragment != "" {
		t.Fatal("test resource unexpectedly has fragment")
	}
}
