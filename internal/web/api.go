package web

import (
	"context"
	"html/template"
	"net/http"
)

func ParseTemplates() (map[string]*template.Template, error) { return parseTemplates() }
func LoadThemeDefaults()                                     { loadThemeDefaults() }
func SetBuildVersion(value string) {
	buildVersion = value
	version = envOr("HMD_VERSION", value)
}
func PollFS(ctx context.Context, content *Store, index *Index, hashes map[string]string, setNamespaces func(NamespaceRegistry), setWikiConfig func(WikiConfig)) {
	pollFS(ctx, content, index, hashes, setNamespaces, setWikiConfig)
}
func Handler(app *App) http.Handler {
	return accessLog(recoverPanic(compression(securityHeaders(app.Auth.Middleware(app.requestSecurity(app.Routes()))))))
}
func CheckReadiness() error { return checkReadiness() }
