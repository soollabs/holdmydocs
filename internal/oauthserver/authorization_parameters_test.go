package oauthserver

import (
	"net/url"
	"slices"
	"testing"
)

func TestAuthorizationIgnoresUnrecognisedParameters(t *testing.T) {
	s, _, _, _, id := newOAuthServiceFixture(t)
	values := url.Values{
		"response_type": {"code"}, "client_id": {id},
		"redirect_uri": {"https://client.example.test/callback"},
		"scope":        {"read"}, "state": {"test-state"},
		"resource":              {s.options.Issuer + mcpResourcePath},
		"code_challenge":        {"822yfK61hJY7zr3yDrwNh0653ur4ZG22pirWUFB9Jtc"},
		"code_challenge_method": {"S256"},
	}
	baseline, err := s.parseAuthorizationRequest(values)
	if err != nil {
		t.Fatal(err)
	}
	values["ui_locales"] = []string{"en-US"}
	values["client_hint"] = []string{"ignored", "also ignored"}
	got, err := s.parseAuthorizationRequest(values)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != baseline.ClientID || got.RedirectURI != baseline.RedirectURI || !slices.Equal(got.Scopes, baseline.Scopes) {
		t.Fatal("client hints changed authorisation")
	}
	values.Add("redirect_uri", "https://attacker.example/callback")
	if _, err := s.parseAuthorizationRequest(values); err == nil {
		t.Fatal("duplicate callback accepted")
	}
}
