package oauthserver

import (
	"encoding/json"
	"net/http"
	"strings"
)

type authorizationServerMetadata struct {
	RegistrationEndpoint                   string   `json:"registration_endpoint,omitempty"`
	Issuer                                 string   `json:"issuer"`
	AuthorizationEndpoint                  string   `json:"authorization_endpoint"`
	TokenEndpoint                          string   `json:"token_endpoint"`
	RevocationEndpoint                     string   `json:"revocation_endpoint"`
	ResponseTypesSupported                 []string `json:"response_types_supported"`
	GrantTypesSupported                    []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported          []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported      []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethodsSupported []string `json:"revocation_endpoint_auth_methods_supported"`
	ScopesSupported                        []string `json:"scopes_supported"`
	AuthorizationResponseIssuerSupported   bool     `json:"authorization_response_iss_parameter_supported"`
}

type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported"`
}

// MetadataHandler serves RFC 8414 and RFC 9728 documents derived only from the
// configured issuer, never request Host or forwarded headers.
func MetadataHandler(opts ServerOptions) http.Handler {
	mux := http.NewServeMux()
	as := authorizationServerMetadata{
		Issuer:                                 opts.Issuer,
		AuthorizationEndpoint:                  opts.Issuer + authorizePath,
		TokenEndpoint:                          opts.Issuer + tokenPath,
		RevocationEndpoint:                     opts.Issuer + revokePath,
		ResponseTypesSupported:                 []string{"code"},
		GrantTypesSupported:                    []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:          []string{"S256"},
		TokenEndpointAuthMethodsSupported:      []string{"none", "client_secret_basic", "client_secret_post"},
		RevocationEndpointAuthMethodsSupported: []string{"none", "client_secret_basic", "client_secret_post"},
		ScopesSupported:                        []string{"read", "write"},
		AuthorizationResponseIssuerSupported:   true,
	}
	if opts.AllowAdminDelegation {
		as.ScopesSupported = append(as.ScopesSupported, "settings")
	}
	if opts.DynamicRegistration {
		as.RegistrationEndpoint = opts.Issuer + registerPath
	}
	resource := protectedResourceMetadata{
		Resource:               opts.Issuer + mcpResourcePath,
		AuthorizationServers:   []string{opts.Issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{"read"},
	}
	write := func(document any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_ = json.NewEncoder(w).Encode(document)
			}
		}
	}
	mux.HandleFunc("GET "+authorizationMetadataPath, write(as))
	mux.HandleFunc("HEAD "+authorizationMetadataPath, write(as))
	mux.HandleFunc("GET "+protectedMetadataPath, write(resource))
	mux.HandleFunc("HEAD "+protectedMetadataPath, write(resource))
	pathSpecific := protectedMetadataPath + mcpResourcePath
	mux.HandleFunc("GET "+pathSpecific, write(resource))
	mux.HandleFunc("HEAD "+pathSpecific, write(resource))
	return mux
}

// ResourceMetadataURL returns the absolute path-specific protected-resource
// metadata URL for an already validated issuer.
func ResourceMetadataURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + protectedMetadataPath + mcpResourcePath
}
