package web

import "testing"

func TestIsLocalURL(t *testing.T) {
	cases := map[string]bool{
		"/blog/post":          true,
		"/blog/post?do=edit":  true,
		"/":                   true,
		"//evil.example":      false,
		"/\\evil.example":     false,
		"https://evil.test":   false,
		"javascript:alert(1)": false,
		"":                    false,
		"blog/post":           false,
	}
	for target, want := range cases {
		if got := isLocalURL(target); got != want {
			t.Errorf("isLocalURL(%q) = %v, want %v", target, got, want)
		}
	}
}
