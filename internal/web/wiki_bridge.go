package web

import "hmd/internal/wiki"

type Page = wiki.Page
type Renderer = wiki.Renderer
type WikiConfig = wiki.WikiConfig

const (
	wikiConfigFile  = wiki.ConfigFile
	defaultSiteName = wiki.DefaultSiteName
)

var (
	Slugify        = wiki.Slugify
	ParseTags      = wiki.ParseTags
	ParsePage      = wiki.ParsePage
	WikiLinks      = wiki.WikiLinks
	NewRenderer    = wiki.NewRenderer
	LoadWikiConfig = wiki.LoadWikiConfig
)

func defaultWikiConfig() WikiConfig     { return wiki.DefaultConfig() }
func validWikiLanding(slug string) bool { return wiki.ValidLanding(slug) }

func pageFile(slug string) string   { return wiki.PageFile(slug) }
func hiddenFile(slug string) string { return wiki.HiddenFile(slug) }
func hiddenSlug(path string) string { return wiki.HiddenSlug(path) }
