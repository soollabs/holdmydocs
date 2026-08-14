package search

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

var ErrBusy = errors.New("search is busy")

func ValidateQuery(query string) error {
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > 512 {
		return fmt.Errorf("search query must be valid UTF-8 and at most 512 characters")
	}
	return nil
}

func validateSearchQuery(query string) error { return ValidateQuery(query) }

var errSearchBusy = ErrBusy
