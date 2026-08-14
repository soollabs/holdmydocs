package app

import (
	"testing"
	"time"
)

func TestBuildCalendarMonth(t *testing.T) {
	ref := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	dates := []string{"2026-07-01", "2026-07-24", "2026-08-01"}

	cal := buildCalendarMonth(ref, "notes", dates)
	if cal.MonthName != "July 2026" {
		t.Errorf("MonthName = %q, want July 2026", cal.MonthName)
	}

	byDate := map[string]CalendarDay{}
	for _, d := range cal.Days {
		if d.Date != "" {
			byDate[d.Date] = d
		}
	}
	if !byDate["2026-07-01"].Lit {
		t.Error("2026-07-01 should be lit")
	}
	if !byDate["2026-07-24"].Lit {
		t.Error("2026-07-24 should be lit")
	}
	if !byDate["2026-07-24"].Today {
		t.Error("2026-07-24 should be marked Today")
	}
	if byDate["2026-07-24"].Slug != "notes/2026-07-24" {
		t.Errorf("2026-07-24 Slug = %q, want notes/2026-07-24", byDate["2026-07-24"].Slug)
	}
	if byDate["2026-07-02"].Lit {
		t.Error("2026-07-02 should not be lit")
	}
	if _, ok := byDate["2026-08-01"]; ok {
		t.Error("August day should not appear in July's grid")
	}
	if len(cal.Days)%7 != 0 {
		t.Errorf("grid length %d should be a multiple of 7", len(cal.Days))
	}
}

func TestBuildCalendarMonthEmpty(t *testing.T) {
	ref := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	cal := buildCalendarMonth(ref, "notes", nil)
	for _, d := range cal.Days {
		if d.Lit {
			t.Errorf("expected no lit days with no dated pages, got %+v", d)
		}
	}
}

func TestBuildWritingStats(t *testing.T) {
	ref := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	dates := []string{
		"2026-07-22", "2026-07-23", "2026-07-24", // 3-day streak ending today
		"2026-07-01", // earlier this month, breaks streak count but not DaysWritten
		"2026-06-30", // last month, shouldn't count toward DaysWritten
	}
	words := func(slug string) int {
		if slug == "notes/2026-07-24" {
			return 42
		}
		return 10
	}

	stats := buildWritingStats(ref, "notes", dates, words)
	if stats.DaysWritten != 4 {
		t.Errorf("DaysWritten = %d, want 4", stats.DaysWritten)
	}
	if stats.Streak != 3 {
		t.Errorf("Streak = %d, want 3", stats.Streak)
	}
	if stats.WordsToday != 42 {
		t.Errorf("WordsToday = %d, want 42", stats.WordsToday)
	}
	if len(stats.Sparkline) != 7 {
		t.Fatalf("Sparkline length = %d, want 7", len(stats.Sparkline))
	}
	if stats.Sparkline[6] != 42 {
		t.Errorf("Sparkline[6] (today) = %d, want 42", stats.Sparkline[6])
	}
}

func TestBuildWritingStatsNoStreak(t *testing.T) {
	ref := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	// Yesterday has no entry, so streak must be 0 even though today does.
	stats := buildWritingStats(ref, "notes", []string{"2026-07-24"}, func(string) int { return 5 })
	if stats.Streak != 1 {
		t.Errorf("Streak = %d, want 1 (today only)", stats.Streak)
	}

	stats2 := buildWritingStats(ref, "notes", []string{"2026-07-20"}, func(string) int { return 5 })
	if stats2.Streak != 0 {
		t.Errorf("Streak = %d, want 0 (no entry today)", stats2.Streak)
	}
}

func TestBuildPrevEntries(t *testing.T) {
	dates := []string{"2026-07-20", "2026-07-21", "2026-07-22", "2026-07-23", "2026-07-24"}
	lines := map[string]string{
		"notes/2026-07-23": "Wrote some code.",
		"notes/2026-07-22": "Read a book.",
		"notes/2026-07-21": "Went for a walk.",
	}
	firstLine := func(slug string) string { return lines[slug] }

	entries := buildPrevEntries("notes/2026-07-24", dates, 3, firstLine)
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	if entries[0].Date != "2026-07-23" || entries[0].FirstLine != "Wrote some code." {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	if entries[0].Slug != "notes/2026-07-23" {
		t.Errorf("entries[0].Slug = %q, want notes/2026-07-23", entries[0].Slug)
	}
	if entries[2].Date != "2026-07-21" {
		t.Errorf("entries[2].Date = %q, want 2026-07-21 (oldest of the 3)", entries[2].Date)
	}
}

func TestBuildPrevEntriesNonDatedPage(t *testing.T) {
	entries := buildPrevEntries("readme", []string{"2026-07-24"}, 3, func(string) string { return "" })
	if entries != nil {
		t.Errorf("expected nil for a non-dated current page, got %v", entries)
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	tests := []struct{ body, want string }{
		{"", ""},
		{"\n\n  \n", ""},
		{"# Heading\nBody text.", "Heading"},
		{"\n\nFirst real line.\nSecond line.", "First real line."},
	}
	for _, tt := range tests {
		if got := firstNonEmptyLine(tt.body); got != tt.want {
			t.Errorf("firstNonEmptyLine(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestCountWords(t *testing.T) {
	if got := countWords("one two three"); got != 3 {
		t.Errorf("countWords = %d, want 3", got)
	}
	if got := countWords(""); got != 0 {
		t.Errorf("countWords(\"\") = %d, want 0", got)
	}
}

func TestDatesInNamespace(t *testing.T) {
	slugs := []string{
		"notes/2026-07-01", "notes/2026-07-24", "notes/not-a-date",
		"blog/2026-07-24", // different namespace, excluded
		"readme",          // root, no namespace match
	}
	got := datesInNamespace("notes", slugs)
	want := []string{"2026-07-01", "2026-07-24"}
	if len(got) != len(want) {
		t.Fatalf("datesInNamespace = %v, want %v", got, want)
	}
	for i, d := range want {
		if got[i] != d {
			t.Errorf("datesInNamespace[%d] = %q, want %q", i, got[i], d)
		}
	}
}
