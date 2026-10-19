package web

import (
	"fmt"
	"html"
	"html/template"
	"strings"

	"hmd/internal/i18n"
)

// translateHTML renders a message that has markup in the middle of a
// sentence. The message stays plain text for translators; each placeholder
// is filled with a fragment built by code, link or similar. Plain string
// arguments are escaped, template.HTML arguments are trusted, and the message
// text itself is always escaped.
func translateHTML(key string, args ...any) (template.HTML, error) {
	if len(args)%2 != 0 {
		return "", fmt.Errorf("tl %q: arguments must be name/value pairs", key)
	}
	fragments := make(map[string]string, len(args)/2)
	plain := make([]any, len(args))
	copy(plain, args)
	for i := 1; i < len(args); i += 2 {
		token := fmt.Sprintf("\x00%d\x00", i/2)
		switch v := args[i].(type) {
		case template.HTML:
			fragments[token] = string(v)
		default:
			fragments[token] = html.EscapeString(fmt.Sprint(v))
		}
		plain[i] = token
	}
	out := html.EscapeString(i18n.T(key, plain...))
	for token, fragment := range fragments {
		out = strings.ReplaceAll(out, token, fragment)
	}
	return template.HTML(out), nil //nolint:gosec // message and arguments are escaped above
}

func codeHTML(text string) template.HTML {
	return template.HTML("<code>" + html.EscapeString(text) + "</code>") //nolint:gosec // escaped
}

func strongHTML(text string) template.HTML {
	return template.HTML("<strong>" + html.EscapeString(text) + "</strong>") //nolint:gosec // escaped
}

// linkHTML builds an anchor. Only site-relative and http(s) targets
// (and the bare "#" placeholder) are allowed so a fragment can never carry a
// javascript: URL.
func linkHTML(href, text string) (template.HTML, error) {
	local := href == "#" || (strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//"))
	if !local && !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "http://") {
		return "", fmt.Errorf("link: unsafe href %q", href)
	}
	return template.HTML(`<a href="` + html.EscapeString(href) + `">` + html.EscapeString(text) + `</a>`), nil //nolint:gosec // escaped
}
