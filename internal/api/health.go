package api

import (
	"context"
	"sort"

	"hmd/internal/wiki"
)

// HealthEntry is one dangling wiki-link target and the caller-visible pages
// that link to it.
type HealthEntry struct {
	Slug    string
	Sources []string
}

// HealthReport is a caller-filtered wiki hygiene report. Missing and Orphans
// are always non-nil so adapters encode an empty list rather than null.
type HealthReport struct {
	Missing []HealthEntry
	Orphans []string
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

// Health reports dangling wiki-links and orphan pages for the caller, filtering
// both the link target and every source by the caller's namespace access. An
// empty namespace reports the whole accessible wiki; a non-empty namespace is
// validated and access-checked first, then limits the report to that namespace.
//
// The namespace roots come from the shared registry snapshot, so sub-namespace
// roots such as index pages are excluded from the orphan list.
func (a *API) Health(ctx context.Context, namespace string) (HealthReport, error) {
	if namespace != "" {
		if !wiki.ValidNamespaceName(namespace) {
			return HealthReport{}, InvalidInput("invalid namespace", nil)
		}
		if !AllowNamespace(ctx, namespace) {
			return HealthReport{}, Forbidden("namespace access denied")
		}
	}

	missing, orphans := a.index.Health(a.Namespaces().IndexSlugs())
	report := HealthReport{Missing: []HealthEntry{}, Orphans: []string{}}
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
