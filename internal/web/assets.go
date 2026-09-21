package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
)

// FS contains the templates and static browser assets served by HMD.
//
//go:embed all:web
var FS embed.FS

// staticVersion is a content hash of every embedded browser asset. Any change
// under web/static changes every versioned URL at once, so cache-busting is
// automatic and a manual version bump can never be forgotten.
var staticVersion = computeStaticVersion()

// computeStaticVersion hashes the sorted path and bytes of every file under
// web/static. Nested files (fonts, vendored bundles) are included, so replacing
// any asset rotates the version.
func computeStaticVersion() string {
	h := sha256.New()
	var paths []string
	_ = fs.WalkDir(FS, "web/static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	for _, path := range paths {
		body, err := FS.ReadFile(path)
		if err != nil {
			continue
		}
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(body)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// StaticVersion is the content hash shared by every versioned asset URL and the
// service worker cache name.
func StaticVersion() string { return staticVersion }

// staticURL returns the live URL for an embedded asset under web/static. The
// content-hash query makes the URL change whenever the asset bytes change.
func staticURL(name string) string {
	return "/_/static/" + name + "?v=" + staticVersion
}
