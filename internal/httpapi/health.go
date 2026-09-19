package httpapi

import (
	"context"
)

// HealthMissingEntry is one dangling wiki-link target and its sources in the
// JSON health response.
type HealthMissingEntry struct {
	Slug    string   `json:"slug"`
	Sources []string `json:"sources"`
}

// HealthReport is the JSON health response. Missing is always a list so an
// empty report encodes as [] rather than null.
type HealthReport struct {
	Missing []HealthMissingEntry `json:"missing"`
	Orphans []string             `json:"orphans"`
}

// healthInput carries the optional namespace scope for the health report.
type healthInput struct {
	Namespace string `json:"namespace"`
}

// health reports the caller-visible wiki hygiene summary.
func (h *Handlers) health(ctx context.Context, in healthInput) (HealthReport, error) {
	report, err := h.api.Health(ctx, in.Namespace)
	if err != nil {
		return HealthReport{}, err
	}
	envelope := HealthReport{Missing: make([]HealthMissingEntry, 0, len(report.Missing)), Orphans: report.Orphans}
	for _, entry := range report.Missing {
		envelope.Missing = append(envelope.Missing, HealthMissingEntry{Slug: entry.Slug, Sources: entry.Sources})
	}
	return envelope, nil
}
