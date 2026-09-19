package web

import (
	"context"
	"html/template"
	"net/http"

	"hmd/internal/api"
)

func ParseTemplates() (map[string]*template.Template, error) { return parseTemplates() }
func LoadThemeDefaults()                                     { loadThemeDefaults() }
func SetBuildVersion(value string) {
	buildVersion = value
	version = envOr("HMD_VERSION", value)
}
func PollFS(ctx context.Context, client *api.API, hashes map[string]string) {
	pollFS(ctx, client, hashes)
}
func Handler(app *App) http.Handler {
	return accessLog(recoverPanic(compression(securityHeaders(app.Auth.Middleware(app.requestSecurity(app.Routes()))))))
}
func CheckReadiness() error { return checkReadiness() }
