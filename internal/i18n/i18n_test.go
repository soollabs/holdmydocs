package i18n

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTranslateWithPlaceholder(t *testing.T) {
	if got := T("error.back_to", "Site", "Docs"); got != "Back to Docs" {
		t.Fatalf("T = %q", got)
	}
}

func TestDates(t *testing.T) {
	if got := MonthYear(time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)); got != "January 2026" {
		t.Errorf("MonthYear = %q", got)
	}
	if got := Date(time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)); got != "2 Sep 2026" {
		t.Errorf("Date = %q", got)
	}
	for m := time.January; m <= time.December; m++ {
		d := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC)
		if got := MonthYear(d) + Date(d); strings.Contains(got, "date.") {
			t.Errorf("%s: missing month message in %q", m, got)
		}
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
