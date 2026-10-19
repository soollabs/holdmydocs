package api

import (
	"fmt"
	"strings"
	"testing"
)

func TestApplyPageEdits(t *testing.T) {
	got, err := ApplyPageEdits("one\ntwo\nthree", []PageEdit{
		{OldText: "one", NewText: "first"},
		{OldText: "two", NewText: ""},
		{OldText: "three", NewText: "last"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "first\n\nlast" {
		t.Fatalf("got %q", got)
	}
	for _, edits := range [][]PageEdit{
		nil,
		{{OldText: "", NewText: "x"}},
		{{OldText: "a", NewText: "x"}},
		{{OldText: "aa", NewText: "x"}},
		{{OldText: "one", NewText: "a"}, {OldText: "one", NewText: "b"}},
	} {
		body := "one one"
		if len(edits) == 1 && edits[0].OldText == "aa" {
			body = "aaa"
		}
		if _, err := ApplyPageEdits(body, edits); err == nil {
			t.Fatalf("invalid edits succeeded: %+v", edits)
		}
	}
}

func TestPageEditDiff(t *testing.T) {
	numbered := func(changed map[int]string) string {
		var body strings.Builder
		for i := 1; i <= 20; i++ {
			line := fmt.Sprintf("line %d", i)
			if replacement, ok := changed[i]; ok {
				line = replacement
			}
			body.WriteString(line + "\n")
		}
		return body.String()
	}
	// Expected hunks match git diff --no-index -U3 without function context.
	tests := []struct {
		name, old, new, want string
	}{
		{"unchanged", "one\ntwo\n", "one\ntwo\n", ""},
		{
			"one word", "one\ntwo\nthree\nfour\nfive\n", "one\ntwo\nTHREE\nfour\nfive\n",
			"@@ -1,5 +1,5 @@\n one\n two\n-three\n+THREE\n four\n five\n",
		},
		{
			"distant changes", numbered(nil), numbered(map[int]string{2: "line 2 changed", 18: "line 18 changed"}),
			"@@ -1,5 +1,5 @@\n line 1\n-line 2\n+line 2 changed\n line 3\n line 4\n line 5\n" +
				"@@ -15,6 +15,6 @@\n line 15\n line 16\n line 17\n-line 18\n+line 18 changed\n line 19\n line 20\n",
		},
		{
			"nearby changes", numbered(nil), numbered(map[int]string{2: "line 2 changed", 9: "line 9 changed"}),
			"@@ -1,12 +1,12 @@\n line 1\n-line 2\n+line 2 changed\n line 3\n line 4\n line 5\n line 6\n line 7\n line 8\n" +
				"-line 9\n+line 9 changed\n line 10\n line 11\n line 12\n",
		},
		{
			"added and removed lines", "a\nb\nc\nd\n", "a\nc\nd\nnew\n",
			"@@ -1,4 +1,4 @@\n a\n-b\n c\n d\n+new\n",
		},
		{
			"no final newline", "a\nb", "a\nc",
			"@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n",
		},
		{"one line", "a\n", "b\n", "@@ -1 +1 @@\n-a\n+b\n"},
		{"from empty", "", "a\n", "@@ -0,0 +1 @@\n+a\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := test.want
			if want != "" {
				want = "--- body (before)\n+++ body (after)\n" + want
			}
			if got := PageEditDiff(test.old, test.new); got != want {
				t.Fatalf("PageEditDiff() =\n%s\nwant\n%s", got, want)
			}
		})
	}
}
