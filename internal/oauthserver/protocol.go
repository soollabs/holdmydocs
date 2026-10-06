package oauthserver

const (
	authorizationMetadataPath = "/.well-known/oauth-authorization-server"
	protectedMetadataPath     = "/.well-known/oauth-protected-resource"
	authorizePath             = "/_/oauth/authorize"
	tokenPath                 = "/_/oauth/token"
	revokePath                = "/_/oauth/revoke"
	mcpResourcePath           = "/_/mcp"
)

type ServerOptions struct {
	Issuer               string
	AllowAdminDelegation bool
}

type ProtocolError struct {
	Code        string
	Description string
	Status      int
}

func (e *ProtocolError) Error() string {
	return e.Code + ": " + e.Description
}
