package oauthserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestMetadataDocumentsUseConfiguredIssuer(t *testing.T) {
	const issuer = "https://wiki.example.test"
	handler := MetadataHandler(ServerOptions{Issuer: issuer})
	request := httptest.NewRequest(http.MethodGet, authorizationMetadataPath, nil)
	request.Host = "attacker.example"
	request.Header.Set("X-Forwarded-Host", "also-attacker.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("AS metadata status = %d", response.Code)
	}
	var as authorizationServerMetadata
	if err := json.Unmarshal(response.Body.Bytes(), &as); err != nil {
		t.Fatal(err)
	}
	if as.Issuer != issuer || as.AuthorizationEndpoint != issuer+authorizePath ||
		as.TokenEndpoint != issuer+tokenPath || !as.AuthorizationResponseIssuerSupported {
		t.Fatalf("AS metadata contains wrong configured identity: %#v", as)
	}
	if reflect.DeepEqual(as.ScopesSupported, []string{"read", "write", "settings"}) {
		t.Fatal("settings scope advertised when administrator delegation is disabled")
	}

	request = httptest.NewRequest(http.MethodGet, protectedMetadataPath+mcpResourcePath, nil)
	request.Host = "attacker.example"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var resource protectedResourceMetadata
	if err := json.Unmarshal(response.Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	if resource.Resource != issuer+mcpResourcePath ||
		!reflect.DeepEqual(resource.AuthorizationServers, []string{issuer}) {
		t.Fatalf("protected resource metadata is wrong: %#v", resource)
	}
}

func TestMetadataAdminScopeOnlyWhenEnabled(t *testing.T) {
	handler := MetadataHandler(ServerOptions{Issuer: "https://wiki.example.test", AllowAdminDelegation: true})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, authorizationMetadataPath, nil))
	var metadata authorizationServerMetadata
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metadata.ScopesSupported, []string{"read", "write", "settings"}) {
		t.Fatalf("scopes = %v", metadata.ScopesSupported)
	}
}

func TestMetadataHeadAndMethodPolicy(t *testing.T) {
	handler := MetadataHandler(ServerOptions{Issuer: "https://wiki.example.test"})
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, authorizationMetadataPath, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD response = %d, body length %d", head.Code, head.Body.Len())
	}
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, authorizationMetadataPath, nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST response = %d, Allow=%q", post.Code, post.Header().Get("Allow"))
	}
}
