package web

import "embed"

// FS contains the templates and static browser assets served by HMD.
//
//go:embed all:web
var FS embed.FS
