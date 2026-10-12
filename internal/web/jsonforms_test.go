package web

import (
	"net/url"
	"strconv"
	"strings"

	"hmd/internal/config"
)

// splitCSV converts a form's comma- or newline-separated field into the JSON
// string array the httpapi mutation surface takes.
func splitCSV(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' })
}

func appearanceJSON(form url.Values) map[string]any {
	return map[string]any{
		"palette":          form.Get("palette"),
		"font_ui":          form.Get("font_ui"),
		"font_mono":        form.Get("font_mono"),
		"skin":             form.Get("skin"),
		"palette_explicit": form.Get("palette_explicit") == "1",
	}
}

// serverSettingsJSON converts an old-style settings form into the payload the
// settings screen posts: values keyed by config.yaml setting key, typed from
// the schema. Only fields present in the form are sent.
func serverSettingsJSON(form url.Values) map[string]any {
	legacy := map[string]string{
		"remote_url": "git.remote_url", "git_user": "git.user", "git_author": "git.author",
		"git_token_file": "git.token_file", "mcp_enabled": "mcp.enabled", "document_model": "document_search.model",
		"document_model_dir": "document_search.model_dir", "document_index_dir": "document_search.index_dir",
		"bind": "server.bind", "repo_dir": "server.repo_dir", "base_url": "server.base_url", "trusted_proxies": "server.trusted_proxies",
		"debug": "server.debug", "max_upload_bytes": "server.max_upload_bytes", "skin": "server.skin", "default_branch": "git.branch",
		"sync_mode": "git.sync_mode", "sync_poll_ms": "git.sync_poll_ms",
	}
	settings := map[string]any{}
	for name := range form {
		key := name
		if k, ok := legacy[name]; ok {
			key = k
		} else if after, ok := strings.CutPrefix(name, "oidc_"); ok {
			key = "oidc." + after
		}
		s, ok := config.Lookup(key)
		if !ok {
			continue
		}
		v := form.Get(name)
		switch s.Type {
		case "boolean":
			settings[key] = v == "on"
		case "integer":
			n, _ := strconv.ParseInt(v, 10, 64)
			settings[key] = n
		case "array":
			settings[key] = splitCSV(v)
		default:
			settings[key] = v
		}
	}
	return map[string]any{"settings": settings}
}

func wikiConfigJSON(form url.Values) map[string]any {
	return map[string]any{
		"site_name": form.Get("site_name"),
		"landing":   form.Get("landing"),
	}
}

func namespaceSaveJSON(form url.Values) map[string]any {
	return map[string]any{
		"name":        strings.Trim(form.Get("name"), "/"),
		"template":    form.Get("template"),
		"base_hash":   form.Get("basehash"),
		"title":       form.Get("title"),
		"description": form.Get("description"),
		"index":       form.Get("index"),
		"tree":        splitCSV(form.Get("tree")),
		"public":      form.Get("public") == "on",
		"skin":        form.Get("skin"),
		"palette":     form.Get("palette"),
		"new_enabled": form.Get("new_enabled") == "on",
		"slug_preset": form.Get("slug_preset"),
		"slug_custom": form.Get("slug_custom"),
		"widgets":     splitCSV(form.Get("widgets")),
	}
}

func namespaceActionJSON(form url.Values) map[string]any {
	return map[string]any{"name": strings.Trim(form.Get("name"), "/")}
}

func tokenCreateJSON(form url.Values) map[string]any {
	return map[string]any{
		"label":      form.Get("label"),
		"expiry":     form.Get("expiry"),
		"scopes":     form["scopes"],
		"namespaces": form["namespaces"],
	}
}

func userCreateJSON(form url.Values) map[string]any {
	return map[string]any{
		"name":     form.Get("name"),
		"password": form.Get("password"),
		"scopes":   form["scopes"],
	}
}

func userScopesJSON(form url.Values) map[string]any {
	return map[string]any{"name": form.Get("name"), "scopes": form["scopes"]}
}

func revokeTokenJSON(form url.Values) map[string]any {
	return map[string]any{"label": form.Get("label")}
}

func emptyJSON(_ url.Values) map[string]any { return map[string]any{} }

func setupJSON(form url.Values) map[string]any {
	return map[string]any{
		"action":            form.Get("action"),
		"setup_wiki":        form.Get("setup_wiki") == "on",
		"default_namespace": form.Get("default_namespace"),
		"new_namespace":     form.Get("new_namespace"),
		"site_name":         form.Get("site_name"),
		"add_namespace":     form.Get("add_namespace") == "on",
		"namespace":         form.Get("namespace"),
		"add_help":          form.Get("add_help") == "on",
	}
}
