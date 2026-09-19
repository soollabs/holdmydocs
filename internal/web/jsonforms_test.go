package web

import (
	"net/url"
	"strconv"
	"strings"
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

func serverSettingsJSON(form url.Values) map[string]any {
	maxUpload, _ := strconv.ParseInt(form.Get("max_upload_bytes"), 10, 64)
	syncPoll, _ := strconv.Atoi(form.Get("sync_poll_ms"))
	return map[string]any{
		"bind":                         form.Get("bind"),
		"repo_dir":                     form.Get("repo_dir"),
		"max_upload_bytes":             maxUpload,
		"sync_poll_ms":                 syncPoll,
		"sync_mode":                    form.Get("sync_mode"),
		"default_branch":               form.Get("default_branch"),
		"skin":                         form.Get("skin"),
		"debug":                        form.Get("debug") == "on",
		"base_url":                     form.Get("base_url"),
		"trusted_proxies":              splitCSV(form.Get("trusted_proxies")),
		"remote_url":                   form.Get("remote_url"),
		"git_user":                     form.Get("git_user"),
		"git_author":                   form.Get("git_author"),
		"git_token":                    form.Get("git_token"),
		"store_git_token":              form.Get("store_git_token") == "on",
		"git_token_file":               form.Get("git_token_file"),
		"mcp_enabled":                  form.Get("mcp_enabled") == "on",
		"document_model":               form.Get("document_model"),
		"document_model_dir":           form.Get("document_model_dir"),
		"document_index_dir":           form.Get("document_index_dir"),
		"oidc_issuer":                  form.Get("oidc_issuer"),
		"oidc_client_id":               form.Get("oidc_client_id"),
		"oidc_client_secret":           form.Get("oidc_client_secret"),
		"oidc_client_secret_file":      form.Get("oidc_client_secret_file"),
		"oidc_local_login":             form.Get("oidc_local_login") == "on",
		"oidc_button_text":             form.Get("oidc_button_text"),
		"oidc_icon":                    form.Get("oidc_icon"),
		"oidc_default_scopes":          splitCSV(form.Get("oidc_default_scopes")),
		"oidc_allowed_subjects":        splitCSV(form.Get("oidc_allowed_subjects")),
		"oidc_allowed_email_domains":   splitCSV(form.Get("oidc_allowed_email_domains")),
		"oidc_allow_any_authenticated": form.Get("oidc_allow_any_authenticated") == "on",
		"oidc_allow_insecure_loopback": form.Get("oidc_allow_insecure_loopback") == "on",
	}
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
