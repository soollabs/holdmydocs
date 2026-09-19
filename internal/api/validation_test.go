package api

import (
	"strings"
	"testing"
)

func TestValidatePageInput(t *testing.T) {
	tags := make([]string, maxPageTags)
	for i := range tags {
		tags[i] = "tag"
	}
	if err := ValidatePageInput(strings.Repeat("t", maxPageTitleRunes), tags, strings.Repeat("x", MaxPageBodyBytes)); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := ValidatePageInput(strings.Repeat("t", maxPageTitleRunes+1), nil, ""); err == nil {
		t.Error("accepted oversized title")
	}
	if err := ValidatePageInput("", nil, strings.Repeat("x", MaxPageBodyBytes+1)); err == nil {
		t.Error("accepted oversized body")
	}
	if err := ValidatePageInput("", append(tags, "one-more"), ""); err == nil {
		t.Error("accepted too many tags")
	}
}
