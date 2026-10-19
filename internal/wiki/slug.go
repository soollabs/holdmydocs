package wiki

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const reservedNamespace = "_"

var reservedTopLevel = map[string]bool{reservedNamespace: true, "attachments": true}

func NamespaceFor(slug string) (namespace, rest string) {
	before, after, ok := strings.Cut(slug, "/")
	if !ok {
		return slug, ""
	}
	return before, after
}

func NamespaceSlug(namespace, rest string) string { return namespace + "/" + rest }

func validPathText(name string) bool {
	return utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl)
}

// checkPathSegment reports why name cannot be one segment of a namespace or
// page path, or nil when it can.
func checkPathSegment(name string) error {
	switch {
	case name == "":
		return errors.New("a segment is empty")
	case !validPathText(name):
		return fmt.Errorf("segment %q contains invalid UTF-8 or control characters", name)
	case strings.Contains(name, `\`):
		return fmt.Errorf("segment %q contains a backslash", name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("segment %q starts with \".\", which is reserved", name)
	case strings.HasPrefix(name, reservedNamespace):
		return fmt.Errorf("segment %q starts with %q, which is reserved", name, reservedNamespace)
	}
	return nil
}

// CheckNamespaceName reports why name is not a usable namespace name, or nil
// when it is.
func CheckNamespaceName(name string) error {
	if strings.Contains(name, "/") {
		return fmt.Errorf("namespace %q contains \"/\"", name)
	}
	if err := checkPathSegment(name); err != nil {
		return err
	}
	if reservedTopLevel[name] {
		return fmt.Errorf("namespace %q is reserved", name)
	}
	return nil
}

func ValidNamespaceName(name string) bool { return CheckNamespaceName(name) == nil }

func ValidPageSegment(name string) bool {
	return !strings.Contains(name, "/") && checkPathSegment(name) == nil
}

func ValidPagePath(rest string) bool {
	if rest == "" {
		return false
	}
	for segment := range strings.SplitSeq(rest, "/") {
		if !ValidPageSegment(segment) {
			return false
		}
	}
	return true
}

// CheckPageSlug reports why slug is not a usable namespace/page identifier,
// or nil when it is. The message names the offending segment so a caller can
// correct it.
func CheckPageSlug(slug string) error {
	namespace, rest, ok := strings.Cut(slug, "/")
	if !ok || namespace == "" || rest == "" {
		return errors.New("a page identifier needs a namespace and a page name separated by \"/\"")
	}
	if err := CheckNamespaceName(namespace); err != nil {
		return err
	}
	for segment := range strings.SplitSeq(rest, "/") {
		if err := checkPathSegment(segment); err != nil {
			return err
		}
	}
	return nil
}

func ValidPageSlug(slug string) bool { return CheckPageSlug(slug) == nil }
