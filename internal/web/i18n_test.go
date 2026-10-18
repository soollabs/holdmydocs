package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"hmd/internal/i18n"
)

var templateKey = regexp.MustCompile(`\b(?:t|tn|tl) "([^"]+)"`)

func TestTemplateMessageKeysExist(t *testing.T) {
	known := i18n.Keys(i18n.Default)
	err := fs.WalkDir(webFS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		src, err := fs.ReadFile(webFS, path)
		if err != nil {
			return err
		}
		for _, m := range templateKey.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(known, m[1]) {
				t.Errorf("%s uses message %q missing from %s", path, m[1], i18n.Default)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var scriptKey = regexp.MustCompile(`'(js\.[a-z_.]+)'`)

func TestScriptMessageKeysExist(t *testing.T) {
	known := i18n.Keys(i18n.Default)
	files, err := fs.Glob(webFS, "web/static/*.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		src, err := fs.ReadFile(webFS, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range scriptKey.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(known, m[1]) {
				t.Errorf("%s uses message %q missing from %s", path, m[1], i18n.Default)
			}
		}
	}
}

var goKey = regexp.MustCompile(`i18n\.(?:T|TN)\(\s*"([^"]+)"`)

func TestGoMessageKeysExist(t *testing.T) {
	known := i18n.Keys(i18n.Default)
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range goKey.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(known, m[1]) {
				t.Errorf("%s uses message %q missing from %s", path, m[1], i18n.Default)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
