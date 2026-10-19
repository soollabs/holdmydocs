package web

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"hmd/internal/testhttp"
)

var draftNoticeTag = regexp.MustCompile(`<[^>]*\bid="appearance-draft"[^>]*>`)

// draftNotice returns the opening tag of the unsaved appearance notice.
func draftNotice(t *testing.T, body string) string {
	t.Helper()
	tag := draftNoticeTag.FindString(body)
	if tag == "" {
		t.Fatal("page has no unsaved appearance notice")
	}
	return tag
}

// TestAppearanceDraft pins how a browser-held appearance draft is rendered:
// it shows on every page without touching the saved preferences, a draft for
// another user, with an unknown value or made over an older save is ignored,
// and a draft that matches the saved appearance is not reported as unsaved.
func TestAppearanceDraft(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	get := func(path, draft string) string {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if draft != "" {
			request.AddCookie(&http.Cookie{Name: appearanceCookie, Value: draft})
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("reading %s: %v, %v", path, readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, response.StatusCode)
		}
		return string(body)
	}
	saved := app.Auth.Prefs("admin")
	base := appearanceBase(saved)
	draft := func(user, skin, palette string) string {
		return url.Values{"user": {user}, "base": {base}, "skin": {skin}, "palette": {palette}}.Encode()
	}
	page := "/" + testHome

	body := get(page, draft("admin", "newsprint", "nord"))
	if !strings.Contains(body, `data-skin="newsprint"`) {
		t.Error("draft skin is not applied to the page")
	}
	// The notice is the shared notice component in its banner form, so it
	// lines up and stacks with every other banner. A wrapper element around it
	// would hide it from the banner rules.
	tag := draftNotice(t, body)
	if !strings.Contains(tag, `ui-notice--banner`) || !strings.Contains(tag, `ui-notice--info`) || strings.Contains(tag, "hidden") {
		t.Errorf("unsaved draft is not announced as a visible info banner: %s", tag)
	}
	if got := app.Auth.Prefs("admin"); got.Skin != saved.Skin || got.Palette != saved.Palette || got.FontUI != saved.FontUI || got.FontMono != saved.FontMono {
		t.Errorf("rendering a draft changed the saved preferences: %+v", got)
	}

	for name, cookie := range map[string]string{
		"other user":      draft("someone", "newsprint", "nord"),
		"unknown skin":    draft("admin", "nope", "nord"),
		"unknown palette": draft("admin", "newsprint", "nope"),
		"unknown font":    url.Values{"user": {"admin"}, "base": {base}, "font_ui": {"nope"}}.Encode(),
		// A save made since, in another browser say, replaces the draft.
		"stale base": url.Values{"user": {"admin"}, "base": {"newsprint|nord||"}, "skin": {"newsprint"}, "palette": {"nord"}}.Encode(),
	} {
		body := get(page, cookie)
		if strings.Contains(body, `data-skin="newsprint"`) || !strings.Contains(draftNotice(t, body), "hidden") {
			t.Errorf("%s: draft was not ignored", name)
		}
	}

	cfg := app.Config()
	same := draft("admin", cfg.Skin, "")
	if body := get(page, same); !strings.Contains(draftNotice(t, body), "hidden") {
		t.Error("a draft equal to the saved appearance is reported as unsaved")
	}

	form := get("/_/settings/appearance", draft("admin", "newsprint", "nord"))
	if !strings.Contains(form, `id="appearance-cancel"`) {
		t.Error("appearance Cancel has no id, so it cannot drop the draft")
	}
	if !strings.Contains(form, "hmdAppearanceDraft.write(") || !strings.Contains(form, `"`+base+`"`) {
		t.Error("settings page does not write the saved appearance into the draft")
	}
	if !strings.Contains(form, `id="theme-style"`) {
		t.Error("theme style has no id, so the settings page cannot read the skin's own fonts")
	}
	if !strings.Contains(form, `name="skin" value="newsprint" checked`) || !strings.Contains(form, `name="palette" value="nord" checked`) {
		t.Error("settings form does not show the draft selection")
	}
}

// TestReadOnlyAppearanceStaysEditable pins that a read-only instance leaves
// appearance open, since it is kept in the browser and never saved, while the
// other settings stay disabled.
func TestReadOnlyAppearanceStaysEditable(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	cfg := app.Config()
	cfg.ReadOnly = true
	app.SetConfig(cfg)
	get := func(path string) string {
		t.Helper()
		response, err := testhttp.Get(t, client, server.URL+path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("reading %s: %v, %v", path, readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, response.StatusCode)
		}
		return string(body)
	}
	body := get("/_/settings/appearance")
	for _, absent := range []string{`data-dismiss="read-only"`, "settings-view-only", `id="appearance-draft"`, `form="appearance-form"`} {
		if strings.Contains(body, absent) {
			t.Errorf("read-only appearance page contains %q", absent)
		}
	}
	if !strings.Contains(body, `name="skin"`) {
		t.Error("read-only appearance page lost its skin picker")
	}
	if other := get("/_/settings/git-author"); !strings.Contains(other, `<fieldset class="settings-view-only" disabled>`) {
		t.Error("git author settings are editable in read-only mode")
	}
}
