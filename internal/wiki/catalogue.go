package wiki

// TagCount is a tag's display name, canonical slug and visible page count.
type TagCount struct {
	Tag   string
	Slug  string
	Count int
}

// BacklinkEntry is a page reference used in navigation and backlink lists.
type BacklinkEntry struct{ Slug, Title string }
