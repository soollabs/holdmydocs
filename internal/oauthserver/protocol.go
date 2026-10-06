package oauthserver

const (
	authorizationMetadataPath = "/.well-known/oauth-authorization-server"
	protectedMetadataPath     = "/.well-known/oauth-protected-resource"
	authorizePath             = "/_/oauth/authorize"
	tokenPath                 = "/_/oauth/token"
	revokePath                = "/_/oauth/revoke"
	registerPath              = "/_/oauth/register"
	mcpResourcePath           = "/_/mcp"
)

type ServerOptions struct {
	Issuer               string
	AllowAdminDelegation bool
	DynamicRegistration  bool
}

type ProtocolError struct {
	Code        string
	Description string
	Status      int
	// RedirectURI and State carry an authorisation error back to the client
	// once its redirect URI has been exactly validated (RFC 6749 section
	// 4.1.2.1). They are empty for errors that must not redirect.
	RedirectURI string
	State       string
}

func (e *ProtocolError) Error() string {
	return e.Code + ": " + e.Description
}
