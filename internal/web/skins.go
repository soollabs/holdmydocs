package web

import (
	"html/template"
	"net/http"
	"net/url"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/presentation"
)

// The skin catalogue and its validation live in internal/presentation; these
// local names keep the rendering code readable.
type skin = presentation.Skin

const defaultSkin = presentation.DefaultSkin

var skinNames = presentation.SkinNames

var skins = presentation.Skins

func resolveSkin(name string) skin { return presentation.ResolveSkin(name) }

func skinName(name string) string { return presentation.SkinName(name) }

func effectiveSkin(cfg config.Config, prefs auth.UserRecord) (string, skin) {
	name := prefs.Skin
	if name == "" {
		name = cfg.Skin
	}
	return skinName(name), resolveSkin(name)
}

func effectivePalette(prefs auth.UserRecord, s skin) string {
	if prefs.Palette != "" {
		return prefs.Palette
	}
	return s.Palette
}

func skinPalettes() map[string]string { return presentation.SkinPalettes() }

// publicAppearance returns the skin and palette style anonymous viewers of a
// public namespace see: the namespace's own choices, falling back to the
// server skin and that skin's palette.
func (app *App) publicAppearance(nsSkin, nsPalette string) (string, template.CSS) {
	name, s := effectiveSkin(app.config(), auth.UserRecord{Skin: nsSkin})
	return name, buildThemeStyle(auth.UserRecord{Palette: effectivePalette(auth.UserRecord{Palette: nsPalette}, s)})
}

// appearanceCookie holds an appearance draft: a skin, palette and fonts that
// one browser applies before they are saved. The settings page writes it as
// the person picks, so every page renders the draft. Saving discards it. A
// read-only instance never saves, so there the draft is the preference. It is
// keyed by user so a shared browser does not hand one person's draft to the
// next, and every value is checked against the catalogues before use. It also
// records the saved appearance it started from, so a later save made anywhere
// else replaces it instead of hiding under it.
const appearanceCookie = "hmd_appearance"

// appearanceBase names a saved appearance, so a draft can tell whether the
// appearance it was made over is still the saved one.
func appearanceBase(saved auth.UserRecord) string {
	return saved.Skin + "|" + saved.Palette + "|" + saved.FontUI + "|" + saved.FontMono
}

// appearanceDraft returns the draft held by the request's browser for user,
// made over the saved appearance.
func appearanceDraft(r *http.Request, user string, saved auth.UserRecord) (draft auth.UserRecord, ok bool) {
	cookie, err := r.Cookie(appearanceCookie)
	if err != nil || user == "" {
		return draft, false
	}
	values, err := url.ParseQuery(cookie.Value)
	if err != nil || values.Get("user") != user || values.Get("base") != appearanceBase(saved) {
		return draft, false
	}
	draft.Skin = values.Get("skin")
	draft.Palette = values.Get("palette")
	draft.FontUI = values.Get("font_ui")
	draft.FontMono = values.Get("font_mono")
	if draft.Skin != "" && !presentation.ValidSkin(draft.Skin) ||
		draft.Palette != "" && !presentation.ValidPalette(draft.Palette) ||
		draft.FontUI != "" && !presentation.ValidFont(draft.FontUI) ||
		draft.FontMono != "" && !presentation.ValidFont(draft.FontMono) {
		return auth.UserRecord{}, false
	}
	return draft, true
}

// userPrefs returns the display preferences to render for the request: the
// saved ones with this browser's draft laid over them. unsaved reports a draft
// that changes what is shown.
func (app *App) userPrefs(r *http.Request) (prefs auth.UserRecord, unsaved bool) {
	user := app.currentUser(r)
	saved := app.Auth.Prefs(user)
	draft, ok := appearanceDraft(r, user, saved)
	if !ok {
		return saved, false
	}
	cfg := app.config()
	savedSkin, savedSkinDef := effectiveSkin(cfg, saved)
	savedPalette := effectivePalette(saved, savedSkinDef)
	prefs = saved
	prefs.Skin, prefs.Palette, prefs.FontUI, prefs.FontMono = draft.Skin, draft.Palette, draft.FontUI, draft.FontMono
	skinNow, skinDef := effectiveSkin(cfg, prefs)
	unsaved = skinNow != savedSkin || effectivePalette(prefs, skinDef) != savedPalette ||
		draft.FontUI != saved.FontUI || draft.FontMono != saved.FontMono
	return prefs, unsaved
}
