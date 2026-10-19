package wiki

import (
	"time"

	"hmd/internal/i18n"
)

// RelativeTime renders a timestamp as a short human age ("3 hours ago",
// "just now"), falling back to a plain date once it is more than a week old.
// It is shared by the browser adapters so every surface words ages the same.
func RelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T("age.just_now")
	case d < time.Hour:
		n := int(d / time.Minute)
		return i18n.TN("age.minutes_ago", n, "Count", n)
	case d < 24*time.Hour:
		n := int(d / time.Hour)
		return i18n.TN("age.hours_ago", n, "Count", n)
	case d < 7*24*time.Hour:
		n := int(d / (24 * time.Hour))
		return i18n.TN("age.days_ago", n, "Count", n)
	default:
		return t.Format("2006-01-02")
	}
}
