package main

import (
	"sort"
	"strings"
	"time"
)

const dailyDatePrefix = "daily/"

// dailyDate extracts the "YYYY-MM-DD" part of a "daily/YYYY-MM-DD" slug, or
// "" if slug isn't in that namespace.
func dailyDate(slug string) string {
	if !strings.HasPrefix(slug, dailyDatePrefix) {
		return ""
	}
	return strings.TrimPrefix(slug, dailyDatePrefix)
}

// CalendarDay is one cell in the calendar widget's month grid.
type CalendarDay struct {
	Day   int    // 1-31, 0 for a leading/trailing blank cell
	Date  string // "YYYY-MM-DD", "" for a blank cell
	Lit   bool   // has an entry
	Today bool
}

// CalendarMonth is the calendar widget's data: a month name and a 7-wide
// grid of days (Monday-first), padded with blank cells fore and aft.
type CalendarMonth struct {
	MonthName string
	Days      []CalendarDay
}

// buildCalendarMonth lays out ref's month, marking days present in
// dailySlugs (from Store.DailyPages) as lit.
func buildCalendarMonth(ref time.Time, dailySlugs []string) CalendarMonth {
	lit := make(map[string]bool, len(dailySlugs))
	for _, slug := range dailySlugs {
		if d := dailyDate(slug); d != "" {
			lit[d] = true
		}
	}

	year, month, _ := ref.Date()
	first := time.Date(year, month, 1, 0, 0, 0, 0, ref.Location())
	daysInMonth := first.AddDate(0, 1, -1).Day()
	todayStr := ref.Format("2006-01-02")

	// Monday-first leading blanks: Go's Weekday has Sunday=0, so shift.
	leading := (int(first.Weekday()) + 6) % 7

	var days []CalendarDay
	for i := 0; i < leading; i++ {
		days = append(days, CalendarDay{})
	}
	for d := 1; d <= daysInMonth; d++ {
		date := first.AddDate(0, 0, d-1).Format("2006-01-02")
		days = append(days, CalendarDay{Day: d, Date: date, Lit: lit[date], Today: date == todayStr})
	}
	for len(days)%7 != 0 {
		days = append(days, CalendarDay{})
	}

	return CalendarMonth{MonthName: first.Format("January 2006"), Days: days}
}

// WritingStats is the writing-stats widget's data.
type WritingStats struct {
	DaysWritten int // distinct daily/ entries this month
	Streak      int // consecutive days up to and including today with an entry
	WordsToday  int
	Sparkline   []int // word counts for the last 7 days, oldest first
}

// buildWritingStats computes this-month/streak/today stats from the set of
// daily slugs that exist and a slug->word-count lookup (bodyWords reads and
// counts a page's body; kept as a func param so tests don't need a real
// Store).
func buildWritingStats(ref time.Time, dailySlugs []string, bodyWords func(slug string) int) WritingStats {
	exists := make(map[string]bool, len(dailySlugs))
	for _, slug := range dailySlugs {
		if d := dailyDate(slug); d != "" {
			exists[d] = true
		}
	}

	stats := WritingStats{}
	thisMonth := ref.Format("2006-01")
	for date := range exists {
		if strings.HasPrefix(date, thisMonth) {
			stats.DaysWritten++
		}
	}

	for i := 0; ; i++ {
		date := ref.AddDate(0, 0, -i).Format("2006-01-02")
		if !exists[date] {
			break
		}
		stats.Streak++
	}

	stats.WordsToday = bodyWords(dailyDatePrefix + ref.Format("2006-01-02"))

	stats.Sparkline = make([]int, 7)
	for i := 6; i >= 0; i-- {
		date := ref.AddDate(0, 0, -i)
		stats.Sparkline[6-i] = bodyWords(dailyDatePrefix + date.Format("2006-01-02"))
	}

	return stats
}

// countWords is a simple whitespace word count, good enough for a sidebar stat.
func countWords(body string) int {
	return len(strings.Fields(body))
}

// PrevEntry is one row in the prev-entries widget.
type PrevEntry struct {
	Slug      string
	Date      string
	FirstLine string
}

// buildPrevEntries returns up to n daily entries strictly before
// currentSlug's date, newest first, with each entry's first non-empty body
// line. currentSlug that isn't a daily/ slug yields nil (widget renders
// nothing on non-daily pages).
func buildPrevEntries(currentSlug string, dailySlugs []string, n int, firstLine func(slug string) string) []PrevEntry {
	currentDate := dailyDate(currentSlug)
	if currentDate == "" {
		return nil
	}

	dates := make([]string, 0, len(dailySlugs))
	for _, slug := range dailySlugs {
		if d := dailyDate(slug); d != "" && d < currentDate {
			dates = append(dates, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	if len(dates) > n {
		dates = dates[:n]
	}

	entries := make([]PrevEntry, 0, len(dates))
	for _, d := range dates {
		slug := dailyDatePrefix + d
		entries = append(entries, PrevEntry{Slug: slug, Date: d, FirstLine: firstLine(slug)})
	}
	return entries
}

// firstNonEmptyLine returns the first non-blank, non-heading-marker line of
// body, trimmed — used as the one-line preview for prev-entries.
func firstNonEmptyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
