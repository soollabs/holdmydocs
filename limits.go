package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var errSearchBusy = errors.New("search is busy")

const (
	maxFormBytes           = 2 << 20
	maxPageBodyBytes       = 1 << 20
	maxPageTitleRunes      = 256
	maxPageTags            = 32
	maxPageTagRunes        = 64
	maxSearchQueryRunes    = 512
	maxGitAuthorRunes      = 256
	maxNamespaceTitleRunes = 256
)

func validRunes(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max
}

func validatePageInput(title string, tags []string, body string) error {
	if !validRunes(title, maxPageTitleRunes) {
		return fmt.Errorf("title must be at most %d characters", maxPageTitleRunes)
	}
	if !utf8.ValidString(body) || len(body) > maxPageBodyBytes {
		return fmt.Errorf("page body must be valid UTF-8 and at most %d bytes", maxPageBodyBytes)
	}
	if len(tags) > maxPageTags {
		return fmt.Errorf("page must have at most %d tags", maxPageTags)
	}
	for _, tag := range tags {
		if !validRunes(tag, maxPageTagRunes) {
			return fmt.Errorf("tags must be valid UTF-8 and at most %d characters", maxPageTagRunes)
		}
	}
	return nil
}

func validateSearchQuery(query string) error {
	if !validRunes(query, maxSearchQueryRunes) {
		return fmt.Errorf("search query must be valid UTF-8 and at most %d characters", maxSearchQueryRunes)
	}
	return nil
}

func validateGitAuthor(author string) error {
	if !validRunes(author, maxGitAuthorRunes) || strings.ContainsAny(author, "\r\n") {
		return fmt.Errorf("git author must be valid UTF-8, at most %d characters, and one line", maxGitAuthorRunes)
	}
	return nil
}
