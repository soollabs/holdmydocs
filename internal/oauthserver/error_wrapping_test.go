package oauthserver

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestErrorRedirectUnwrapsProtocolError(t *testing.T) {
	protocol := &ProtocolError{
		Code: "invalid_scope", Status: http.StatusBadRequest,
		RedirectURI: "https://client.example/callback", State: "state",
	}
	for _, err := range []error{protocol, fmt.Errorf("authorisation failed: %w", protocol)} {
		redirect, ok := ErrorRedirect(err, "https://wiki.example")
		if !ok {
			t.Fatal("validated callback was lost when unwrapping the error")
		}
		target, parseErr := url.Parse(redirect)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if target.Query().Get("error") != protocol.Code || target.Query().Get("state") != protocol.State {
			t.Fatalf("incorrect error callback: %s", redirect)
		}
	}
	protocol.RedirectURI = ""
	if _, ok := ErrorRedirect(fmt.Errorf("authorisation failed: %w", protocol), "https://wiki.example"); ok {
		t.Fatal("unwrapping must not grant an unvalidated callback")
	}
}
