package api

import (
	"context"
	"sort"
	"strings"
	"time"

	"hmd/internal/wiki"
)

// StalePageDays is the age at which an unchanged page is reported as stale.
const StalePageDays = 180

const stalePageAge = StalePageDays * 24 * time.Hour

// HealthEntry is one dangling wiki-link target and the caller-visible pages
// that link to it.
type HealthEntry struct {
	Slug    string
	Sources []string
}

// StalePageEntry is one accessible page whose latest Git revision is old.
type StalePageEntry struct {
	Slug        string
	LastUpdated time.Time
}

// HealthReport is a caller-filtered wiki hygiene report. All slices are
// non-nil so adapters encode empty lists rather than null.
type HealthReport struct {
	Missing []HealthEntry
	Orphans []string
	Stale   []StalePageEntry
}

// HealthSummary contains the inexpensive link-health counts used in page
// chrome. Stale-page detection walks Git history and is reserved for explicit
// health-report requests.
type HealthSummary struct {
	Missing int
	Orphans int
}

// DocumentsEnabled reports whether document (attachment) search is available,
// so an adapter can register or render attachment-search affordances.
func (a *API) DocumentsEnabled() bool { return a.index != nil && a.index.DocumentsEnabled() }

// Ready reports whether the repository is readable and the search index is
// ready, for the browser readiness endpoint.
func (a *API) Ready() bool {
	if a.store == nil || a.index == nil {
		return false
	}
	if _, err := a.store.List(); err != nil {
		return false
	}
	return a.index.Ready()
}

// Health reports dangling wiki-links, orphan pages and pages whose latest Git
// revision is at least StalePageDays old. It filters all results by the
// caller's namespace access. An empty namespace reports the whole accessible
// wiki; a non-empty namespace is validated and access-checked first, then
// limits the report to that namespace.
//
// The namespace roots come from the shared registry snapshot, so sub-namespace
// roots such as index pages are excluded from the orphan list.
func (a *API) Health(ctx context.Context, namespace string) (HealthReport, error) {
	return a.healthAt(ctx, namespace, time.Now())
}

func (a *API) healthAt(ctx context.Context, namespace string, now time.Time) (HealthReport, error) {
	report, err := a.healthLinks(ctx, namespace)
	if err != nil {
		return HealthReport{}, err
	}
	if a.store == nil {
		return HealthReport{}, Unavailable("page history is unavailable", nil)
	}

	paths, err := a.store.List()
	if err != nil {
		return HealthReport{}, Unavailable("listing pages for health report", err)
	}
	cutoff := now.Add(-stalePageAge)
	for _, path := range paths {
		slug := strings.TrimSuffix(path, ".md")
		if !wiki.ValidPageSlug(slug) || !AllowSlug(ctx, slug) || !inNamespace(slug, namespace) {
			continue
		}
		history, err := a.store.History(path)
		if err != nil {
			return HealthReport{}, Unavailable("reading page history for health report", err)
		}
		if len(history) == 0 || history[0].When.After(cutoff) {
			continue
		}
		report.Stale = append(report.Stale, StalePageEntry{Slug: slug, LastUpdated: history[0].When})
	}
	sort.Slice(report.Stale, func(i, j int) bool {
		if report.Stale[i].LastUpdated.Equal(report.Stale[j].LastUpdated) {
			return report.Stale[i].Slug < report.Stale[j].Slug
		}
		return report.Stale[i].LastUpdated.Before(report.Stale[j].LastUpdated)
	})
	return report, nil
}

// HealthSummary returns caller-visible missing-link and orphan counts without
// walking Git history. It is suitable for request-path UI chrome.
func (a *API) HealthSummary(ctx context.Context) (HealthSummary, error) {
	report, err := a.healthLinks(ctx, "")
	if err != nil {
		return HealthSummary{}, err
	}
	return HealthSummary{Missing: len(report.Missing), Orphans: len(report.Orphans)}, nil
}

// healthLinks reports dangling wiki-links and orphan pages, filtering both
// targets and sources by the caller's namespace access.
func (a *API) healthLinks(ctx context.Context, namespace string) (HealthReport, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return HealthReport{}, err
	}
	if namespace != "" {
		if !wiki.ValidNamespaceName(namespace) {
			return HealthReport{}, InvalidInput("invalid namespace", nil)
		}
		if !AllowNamespace(ctx, namespace) {
			return HealthReport{}, Forbidden("namespace access denied")
		}
	}

	missing, orphans := a.index.Health(a.Namespaces().IndexSlugs())
	report := HealthReport{Missing: []HealthEntry{}, Orphans: []string{}, Stale: []StalePageEntry{}}
	for slug, sources := range missing {
		if !AllowSlug(ctx, slug) || !inNamespace(slug, namespace) {
			continue
		}
		allowedSources := make([]string, 0, len(sources))
		for _, source := range sources {
			if AllowSlug(ctx, source) {
				allowedSources = append(allowedSources, source)
			}
		}
		if len(allowedSources) == 0 {
			continue
		}
		report.Missing = append(report.Missing, HealthEntry{Slug: slug, Sources: allowedSources})
	}
	for _, slug := range orphans {
		if AllowSlug(ctx, slug) && inNamespace(slug, namespace) {
			report.Orphans = append(report.Orphans, slug)
		}
	}
	sort.Slice(report.Missing, func(i, j int) bool { return report.Missing[i].Slug < report.Missing[j].Slug })
	return report, nil
}

// inNamespace reports whether slug belongs to namespace; an empty namespace
// matches every slug.
func inNamespace(slug, namespace string) bool {
	if namespace == "" {
		return true
	}
	ns, _ := wiki.NamespaceFor(slug)
	return ns == namespace
}
