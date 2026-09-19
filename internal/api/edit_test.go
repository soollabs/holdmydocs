package api

import "testing"

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
