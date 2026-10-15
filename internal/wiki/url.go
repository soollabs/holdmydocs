package wiki

import (
	"net/url"
	"strings"
)

// HiddenRoutePrefix is the browser route under which hidden pages are served.
const HiddenRoutePrefix = "/_/hidden"

// PageURL is a page's browser URL. Every link to a page goes through it, so
// the mapping from slug to URL lives in one place.
func PageURL(slug string) string { return "/" + escapePath(slug) }

// HiddenPageURL is the browser URL of a hidden page.
func HiddenPageURL(slug string) string { return HiddenRoutePrefix + PageURL(slug) }

// NamespaceURL is the browser URL of a namespace's index.
func NamespaceURL(namespace string) string { return "/" + url.PathEscape(namespace) + "/" }

// escapePath percent-encodes each segment of a slash-separated path, so a
// page named "a?b" or "a#b" is not cut short by the browser.
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// AttachmentRoutePrefix is the browser route attachments are served under.
const AttachmentRoutePrefix = "/_/attachments/"

// AttachmentURL is the browser URL of a page's attachment.
func AttachmentURL(slug, filename string) string {
	return AttachmentRoutePrefix + escapePath(slug) + "/" + url.PathEscape(filename)
}
