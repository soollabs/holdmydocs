package web

import (
	"html/template"

	"hmd/internal/config"
	"hmd/internal/httpmiddleware"
)

func ParseTemplates() (map[string]*template.Template, error) { return parseTemplates() }
func LoadThemeDefaults()                                     { loadThemeDefaults() }
func SetBuildVersion(value string) {
	buildVersion = value
	version = config.EnvOr("HMD_VERSION", value)
}

// Version returns the build version stamp used by the MCP server identity.
func Version() string { return version }

// Config returns the current runtime configuration snapshot.
func (app *App) Config() config.Config { return app.apiClient().Config() }

// Security builds the request-security middleware for the browser adapter. The
// composition root applies it once around the assembled route tree.
func (app *App) Security() httpmiddleware.Security {
	return httpmiddleware.Security{Config: app.Config, Auth: app.Auth}
}
