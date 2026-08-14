package web

import (
	"sort"
	"strings"
	"time"
)

// dateFormat is the only slug shape calendar/writing-stats/prev-entries
// recognise within a namespace.
const dateFormat = "2006-01-02"

// datesInNamespace extracts the YYYY-MM-DD dates among slugs that belong to
// ns, leniently: a slug whose within-namespace remainder isn't exactly a
// valid date is skipped rather than erroring, so these widgets are inert —
// not broken — in a namespace whose slugs aren't dates.
func datesInNamespace(ns string, slugs []string) []string {
	var dates []string
	for _, slug := range slugs {
		slugNS, rest := namespaceFor(slug)
		if slugNS != ns {
			continue
		}
		if _, err := time.Parse(dateFormat, rest); err != nil {
			continue
		}
		dates = append(dates, rest)
	}
	return dates
}

// CalendarDay is one cell in the calendar widget's month grid.
type CalendarDay struct {
	Day   int    // 1-31, 0 for a leading/trailing blank cell
	Date  string // "YYYY-MM-DD", "" for a blank cell
	Slug  string // full page slug for the day's link, "" for a blank cell
	Lit   bool   // has an entry
	Today bool
}

// CalendarMonth is the calendar widget's data: a month name and a 7-wide
// grid of days (Monday-first), padded with blank cells fore and aft.
type CalendarMonth struct {
	MonthName string
	Days      []CalendarDay
}

// buildCalendarMonth lays out ref's month for namespace ns, marking days
// present in dates (from datesInNamespace) as lit.
func buildCalendarMonth(ref time.Time, ns string, dates []string) CalendarMonth {
	lit := make(map[string]bool, len(dates))
	for _, d := range dates {
		lit[d] = true
	}

	year, month, _ := ref.Date()
	first := time.Date(year, month, 1, 0, 0, 0, 0, ref.Location())
	daysInMonth := first.AddDate(0, 1, -1).Day()
	todayStr := ref.Format(dateFormat)

	// Monday-first leading blanks: Go's Weekday has Sunday=0, so shift.
	leading := (int(first.Weekday()) + 6) % 7

	var days []CalendarDay
	for range leading {
		days = append(days, CalendarDay{})
	}
	for d := 1; d <= daysInMonth; d++ {
		date := first.AddDate(0, 0, d-1).Format(dateFormat)
		days = append(days, CalendarDay{Day: d, Date: date, Slug: namespaceSlug(ns, date), Lit: lit[date], Today: date == todayStr})
	}
	for len(days)%7 != 0 {
		days = append(days, CalendarDay{})
	}

	return CalendarMonth{MonthName: first.Format("January 2006"), Days: days}
}

// WritingStats is the writing-stats widget's data.
type WritingStats struct {
	DaysWritten int // distinct dated entries this month
	Streak      int // consecutive days up to and including today with an entry
	WordsToday  int
	Sparkline   []int // word counts for the last 7 days, oldest first
}

// buildWritingStats computes this-month/streak/today stats for namespace ns
// from its dates (from datesInNamespace) and a slug->word-count lookup
// (bodyWords reads and counts a page's body; kept as a func param so tests
// don't need a real Store).
func buildWritingStats(ref time.Time, ns string, dates []string, bodyWords func(slug string) int) WritingStats {
	exists := make(map[string]bool, len(dates))
	for _, d := range dates {
		exists[d] = true
	}

	stats := WritingStats{}
	thisMonth := ref.Format("2006-01")
	for date := range exists {
		if strings.HasPrefix(date, thisMonth) {
			stats.DaysWritten++
		}
	}

	for i := 0; ; i++ {
		date := ref.AddDate(0, 0, -i).Format(dateFormat)
		if !exists[date] {
			break
		}
		stats.Streak++
	}

	stats.WordsToday = bodyWords(namespaceSlug(ns, ref.Format(dateFormat)))

	stats.Sparkline = make([]int, 7)
	for i := 6; i >= 0; i-- {
		date := ref.AddDate(0, 0, -i)
		stats.Sparkline[6-i] = bodyWords(namespaceSlug(ns, date.Format(dateFormat)))
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

// buildPrevEntries returns up to n dated entries strictly before
// currentSlug's date, newest first, with each entry's first non-empty body
// line. dates must already be scoped to currentSlug's own namespace (see
// datesInNamespace). currentSlug whose within-namespace remainder isn't a
// valid date yields nil (widget renders nothing on a non-dated page).
func buildPrevEntries(currentSlug string, dates []string, n int, firstLine func(slug string) string) []PrevEntry {
	ns, rest := namespaceFor(currentSlug)
	if _, err := time.Parse(dateFormat, rest); err != nil {
		return nil
	}
	currentDate := rest

	earlier := make([]string, 0, len(dates))
	for _, d := range dates {
		if d < currentDate {
			earlier = append(earlier, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(earlier)))
	if len(earlier) > n {
		earlier = earlier[:n]
	}

	entries := make([]PrevEntry, 0, len(earlier))
	for _, d := range earlier {
		slug := namespaceSlug(ns, d)
		entries = append(entries, PrevEntry{Slug: slug, Date: d, FirstLine: firstLine(slug)})
	}
	return entries
}

// firstNonEmptyLine returns the first non-blank, non-heading-marker line of
// body, trimmed — used as the one-line preview for prev-entries.
func firstNonEmptyLine(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
