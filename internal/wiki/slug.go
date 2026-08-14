package wiki

import "strings"

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

func ValidNamespaceName(name string) bool {
	return name != "" && !reservedTopLevel[name] &&
		!strings.ContainsAny(name, `/\`) &&
		!strings.HasPrefix(name, ".") && !strings.HasPrefix(name, reservedNamespace)
}

func ValidPageSegment(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) &&
		!strings.HasPrefix(name, ".") && !strings.HasPrefix(name, reservedNamespace)
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

func ValidPageSlug(slug string) bool {
	namespace, rest := NamespaceFor(slug)
	return ValidNamespaceName(namespace) && ValidPagePath(rest)
}
