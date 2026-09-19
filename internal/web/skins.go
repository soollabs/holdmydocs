package web

import "hmd/internal/presentation"

// The skin catalogue and its validation live in internal/presentation; these
// local names keep the rendering code readable.
type skin = presentation.Skin

const defaultSkin = presentation.DefaultSkin

var skinNames = presentation.SkinNames

var skins = presentation.Skins

func resolveSkin(name string) skin { return presentation.ResolveSkin(name) }

func skinName(name string) string { return presentation.SkinName(name) }

func effectiveSkin(cfg Config, prefs userRecord) (string, skin) {
	name := prefs.Skin
	if name == "" {
		name = cfg.Skin
	}
	return skinName(name), resolveSkin(name)
}

func effectivePalette(prefs userRecord, s skin) string {
	if prefs.Palette != "" {
		return prefs.Palette
	}
	return s.Palette
}

func skinPalettes() map[string]string { return presentation.SkinPalettes() }
