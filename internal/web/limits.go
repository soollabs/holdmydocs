package web

import (
	"fmt"
)

const (
	maxSearchQueryRunes = 512
	maxExportFiles      = 10_000
	maxExportBytes      = 100 << 20
)

func exportBudget(files *int, bytes *int64, size int64) error {
	*files++
	*bytes += size
	if *files > maxExportFiles || *bytes > maxExportBytes {
		return fmt.Errorf("export exceeds %d files or %d bytes", maxExportFiles, maxExportBytes)
	}
	return nil
}
