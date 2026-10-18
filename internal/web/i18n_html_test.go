package web

import (
	"html/template"
	"testing"
)

func TestTranslateHTMLEscapesTextAndStrings(t *testing.T) {
	got, err := translateHTML("base.legacy_attachments.message", "Folder", codeHTML("a&b"), "Link", "<b>x</b>")
	if err != nil {
		t.Fatal(err)
	}
	want := template.HTML("Some attachments are still in the old top-level <code>a&amp;b</code> folder. &lt;b&gt;x&lt;/b&gt;.")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTranslateHTMLNeedsPairs(t *testing.T) {
	if _, err := translateHTML("base.legacy_attachments.message", "Folder"); err == nil {
		t.Fatal("odd argument count accepted")
	}
}

func TestLinkHTMLRejectsUnsafeHref(t *testing.T) {
	for _, href := range []string{"javascript:alert(1)", "//evil.example/x", "data:text/html,x"} {
		if _, err := linkHTML(href, "x"); err == nil {
			t.Errorf("linkHTML accepted %q", href)
		}
	}
	got, err := linkHTML("/_/x?a=1&b=2", "go <now>")
	if err != nil || got != `<a href="/_/x?a=1&amp;b=2">go &lt;now&gt;</a>` {
		t.Fatalf("got %q, %v", got, err)
	}
}
