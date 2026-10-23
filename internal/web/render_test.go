package web

import (
	"slices"
	"testing"

	"hmd/internal/wiki"
)

func TestTopTags(t *testing.T) {
	tags := []wiki.TagCount{
		{Tag: "a", Count: 1}, {Tag: "b", Count: 4}, {Tag: "c", Count: 2},
		{Tag: "d", Count: 4}, {Tag: "e", Count: 3}, {Tag: "f", Count: 1}, {Tag: "g", Count: 5},
	}
	top, more := topTags(tags, 5)
	var got []string
	for _, tag := range top {
		got = append(got, tag.Tag)
	}
	if want := []string{"g", "b", "d", "e", "c"}; !slices.Equal(got, want) || more != 2 {
		t.Errorf("topTags = %v, %d more; want %v, 2 more", got, more, want)
	}
	if top, more := topTags(tags[:3], 5); len(top) != 3 || more != 0 {
		t.Errorf("topTags under the limit = %d tags, %d more; want 3, 0", len(top), more)
	}
	if tags[0].Tag != "a" {
		t.Error("topTags must not reorder its input")
	}
}
