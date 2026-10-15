package wiki

import "testing"

func TestPageURLs(t *testing.T) {
	tests := []struct {
		name, got, want string
	}{
		{"page", PageURL("notes/alpha"), "/notes/alpha"},
		{"nested page", PageURL("notes/2026/alpha"), "/notes/2026/alpha"},
		{"segments are encoded", PageURL(`notes/a?b#c d"e`), "/notes/a%3Fb%23c%20d%22e"},
		{"hidden page", HiddenPageURL("notes/template"), "/_/hidden/notes/template"},
		{"namespace", NamespaceURL("notes"), "/notes/"},
		{"namespace is encoded", NamespaceURL("my notes"), "/my%20notes/"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}
