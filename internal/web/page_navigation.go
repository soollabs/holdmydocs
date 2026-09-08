package web

// pageLink is a reading-navigation destination. Callers supply URL generation
// so the same tree order works for live routes and portable static exports.
type pageLink struct {
	Title string
	Href  string
}

// Only pass a tree built from pages the reader is permitted to see.
func orderedTreePages(root *navNode) []*navNode {
	var pages []*navNode
	for _, node := range root.Children {
		if node.IsPage {
			pages = append(pages, node)
		}
		pages = append(pages, orderedTreePages(node)...)
	}
	return pages
}

func pageNeighbours(pages []*navNode, current string, hrefFor func(string) string) (previous, next *pageLink) {
	link := func(page *navNode) *pageLink {
		return &pageLink{Title: page.Title, Href: hrefFor(page.Path)}
	}
	for i, page := range pages {
		if page.Path != current {
			continue
		}
		if i > 0 {
			previous = link(pages[i-1])
		}
		if i+1 < len(pages) {
			next = link(pages[i+1])
		}
		break
	}
	return previous, next
}
