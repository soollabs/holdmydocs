package web

import (
	"fmt"
	"unicode/utf8"
)

const (
	maxFormBytes        = 2 << 20
	maxSearchQueryRunes = 512
	maxExportFiles      = 10_000
	maxExportBytes      = 100 << 20
)

func validRunes(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max
}

func exportBudget(files *int, bytes *int64, size int64) error {
	*files++
	*bytes += size
	if *files > maxExportFiles || *bytes > maxExportBytes {
		return fmt.Errorf("export exceeds %d files or %d bytes", maxExportFiles, maxExportBytes)
	}
	return nil
}
