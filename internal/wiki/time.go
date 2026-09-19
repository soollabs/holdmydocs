package wiki

import (
	"fmt"
	"time"
)

// RelativeTime renders a timestamp as a short human age ("3 hours ago",
// "just now"), falling back to a plain date once it is more than a week old.
// It is shared by the browser adapters so every surface words ages the same.
func RelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d / time.Minute)
		return fmt.Sprintf("%d minute%s ago", n, Plural(n))
	case d < 24*time.Hour:
		n := int(d / time.Hour)
		return fmt.Sprintf("%d hour%s ago", n, Plural(n))
	case d < 7*24*time.Hour:
		n := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d day%s ago", n, Plural(n))
	default:
		return t.Format("2006-01-02")
	}
}

// Plural returns "s" for any count other than one, for simple English plurals.
func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
