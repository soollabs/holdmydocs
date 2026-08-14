package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageInputLimits(t *testing.T) {
	tags := make([]string, maxPageTags)
	for i := range tags {
		tags[i] = "tag"
	}
	if err := validatePageInput(strings.Repeat("t", maxPageTitleRunes), tags, strings.Repeat("x", maxPageBodyBytes)); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := validatePageInput(strings.Repeat("t", maxPageTitleRunes+1), nil, ""); err == nil {
		t.Error("accepted oversized title")
	}
	if err := validatePageInput("", nil, strings.Repeat("x", maxPageBodyBytes+1)); err == nil {
		t.Error("accepted oversized body")
	}
	if err := validatePageInput("", append(tags, "one-more"), ""); err == nil {
		t.Error("accepted too many tags")
	}
}

func TestRequestSecurityRejectsOversizedForm(t *testing.T) {
	app := &App{}
	app.SetConfig(Config{})
	h := app.requestSecurity(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/_/login", strings.NewReader(strings.Repeat("x", maxFormBytes+1)))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", res.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestSearchQueryLimitAndLiteralSyntax(t *testing.T) {
	if err := validateSearchQuery(strings.Repeat("x", maxSearchQueryRunes)); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := validateSearchQuery(strings.Repeat("x", maxSearchQueryRunes+1)); err == nil {
		t.Error("accepted oversized query")
	}
	ix, err := BuildIndex([]Page{{Slug: "notes/literal", Title: "literal", Body: `alpha +beta`}})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search(`+beta`)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Slug != "notes/literal" {
		t.Errorf("literal query results = %#v", hits)
	}
}

func TestExportBudget(t *testing.T) {
	files, bytes := maxExportFiles-1, int64(maxExportBytes-1)
	if err := exportBudget(&files, &bytes, 1); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := exportBudget(&files, &bytes, 1); err == nil {
		t.Error("accepted oversized export")
	}
}
