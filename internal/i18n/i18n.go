// Package i18n owns every user-facing string that is not wiki content. The
// language is a server-level setting, so one process-wide translator is
// swapped atomically when the setting changes. Catalogues are go-i18n v2 JSON
// files embedded from locales/; en-GB is the source and every other locale is
// translated outside the repository.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// Default is the source language and the fallback for missing translations.
const Default = "en-GB"

//go:embed locales/*.json
var localeFS embed.FS

var bundle = mustBundle()

type translator struct {
	tag       string
	localizer *goi18n.Localizer
}

var active atomic.Pointer[translator]

func init() { active.Store(&translator{Default, goi18n.NewLocalizer(bundle, Default)}) }

func mustBundle() *goi18n.Bundle {
	b := goi18n.NewBundle(language.MustParse(Default))
	b.RegisterUnmarshalFunc("json", json.Unmarshal)
	entries, err := fs.ReadDir(localeFS, "locales")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		if _, err := b.LoadMessageFileFS(localeFS, path.Join("locales", e.Name())); err != nil {
			panic(fmt.Sprintf("i18n: loading %s: %v", e.Name(), err))
		}
	}
	return b
}

// Languages returns the language tags that have a catalogue, sorted.
func Languages() []string {
	tags := make([]string, 0, len(bundle.LanguageTags()))
	for _, tag := range bundle.LanguageTags() {
		tags = append(tags, tag.String())
	}
	slices.Sort(tags)
	return tags
}

// SetLanguage makes tag the active language. An empty tag selects Default.
func SetLanguage(tag string) error {
	if tag == "" {
		tag = Default
	}
	if !slices.Contains(Languages(), tag) {
		return fmt.Errorf("i18n: no catalogue for language %q", tag)
	}
	active.Store(&translator{tag, goi18n.NewLocalizer(bundle, tag, Default)})
	return nil
}

// Language returns the active language tag.
func Language() string { return active.Load().tag }

// T returns the message for key. args are alternating name/value pairs that
// fill the message's {{.Name}} placeholders. A missing key returns the key so
// the gap is visible rather than blank.
func T(key string, args ...any) string {
	return localize(key, nil, args)
}

// TN is T for messages with plural forms; count selects the form and is also
// available to the message as {{.Count}}.
func TN(key string, count int, args ...any) string {
	return localize(key, count, args)
}

func localize(key string, count any, args []any) string {
	cfg := &goi18n.LocalizeConfig{MessageID: key, TemplateData: data(args)}
	if count != nil {
		cfg.PluralCount = count
		cfg.TemplateData.(map[string]any)["Count"] = count
	}
	out, err := active.Load().localizer.Localize(cfg)
	if err != nil && out == "" {
		return key
	}
	return out
}

func data(args []any) map[string]any {
	m := make(map[string]any, len(args)/2+1)
	for i := 0; i+1 < len(args); i += 2 {
		if name, ok := args[i].(string); ok {
			m[name] = args[i+1]
		}
	}
	return m
}

// MonthYear formats t as a month heading, for example "January 2026". The
// month is the standalone form, which some languages decline differently from
// the form used inside a date.
func MonthYear(t time.Time) string {
	return T("date.month_year", "Month", T("date.month."+monthKey(t)), "Year", t.Year())
}

// Date formats t as a day, short month and year, for example "2 Jan 2026".
func Date(t time.Time) string {
	return T("date.day_month_year", "Day", t.Day(), "Month", T("date.month_short."+monthKey(t)), "Year", t.Year())
}

func monthKey(t time.Time) string { return strings.ToLower(t.Month().String()) }

// entry is one catalogue message as stored in the JSON files.
type entry map[string]string

var catalogues = mustCatalogues()

func mustCatalogues() map[string]map[string]entry {
	out := map[string]map[string]entry{}
	entries, err := fs.ReadDir(localeFS, "locales")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		b, err := localeFS.ReadFile(path.Join("locales", e.Name()))
		if err != nil {
			panic(err)
		}
		var raw map[string]entry
		if err := json.Unmarshal(b, &raw); err != nil {
			panic(fmt.Sprintf("i18n: parsing %s: %v", e.Name(), err))
		}
		out[strings.TrimSuffix(e.Name(), ".json")] = raw
	}
	return out
}

// Keys returns the message IDs in a language's catalogue, sorted; tests use it
// to hold catalogues to the en-GB source.
func Keys(tag string) []string {
	keys := make([]string, 0, len(catalogues[tag]))
	for k := range catalogues[tag] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Messages returns the active language's messages whose key starts with
// prefix, for scripts that translate in the browser. A message with only an
// "other" form is a string; one with plural forms is a map of form to text.
// Keys the active language lacks fall back to en-GB.
func Messages(prefix string) map[string]any {
	out := map[string]any{}
	for _, tag := range []string{Default, Language()} {
		for key, e := range catalogues[tag] {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			forms := make(map[string]string, len(e))
			for form, text := range e {
				if form != "description" {
					forms[form] = text
				}
			}
			if len(forms) == 1 && forms["other"] != "" {
				out[key] = forms["other"]
			} else {
				out[key] = forms
			}
		}
	}
	return out
}
