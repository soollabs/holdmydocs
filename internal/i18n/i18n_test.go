package i18n

import (
	"slices"
	"testing"
)

func TestTranslateWithPlaceholder(t *testing.T) {
	if got := T("error.back_to", "Site", "Docs"); got != "Back to Docs" {
		t.Fatalf("T = %q", got)
	}
}

func TestMissingKeyReturnsKey(t *testing.T) {
	if got := T("no.such.key"); got != "no.such.key" {
		t.Fatalf("T = %q, want the key", got)
	}
}

func TestSetLanguage(t *testing.T) {
	if err := SetLanguage(""); err != nil || Language() != Default {
		t.Fatalf("empty tag: err=%v language=%q", err, Language())
	}
	if err := SetLanguage("xx"); err == nil {
		t.Fatal("unknown language accepted")
	}
	if Language() != Default {
		t.Fatalf("failed SetLanguage changed the language to %q", Language())
	}
}

func TestSourceCatalogueExists(t *testing.T) {
	if !slices.Contains(Languages(), Default) {
		t.Fatalf("no %s catalogue in %v", Default, Languages())
	}
}

// Every catalogue key must exist in the en-GB source. Completeness is not
// required: Weblate leaves languages partly translated and missing keys fall
// back to en-GB.
func TestCataloguesHaveNoExtraKeys(t *testing.T) {
	source := Keys(Default)
	for _, tag := range Languages() {
		for _, key := range Keys(tag) {
			if !slices.Contains(source, key) {
				t.Errorf("%s has key %q that %s lacks", tag, key, Default)
			}
		}
	}
}

func TestPluralForms(t *testing.T) {
	for count, want := range map[int]string{0: "0 words today", 1: "1 word today", 2: "2 words today"} {
		if got := TN("base.statusline.words_today", count, "Count", count); got != want {
			t.Errorf("TN(%d) = %q, want %q", count, got, want)
		}
	}
}
