package web

import "hmd/internal/search"

type Index = search.Index
type TagCount = search.TagCount
type SearchHit = search.SearchHit
type DocumentSearch = search.DocumentSearch
type AttachmentSearchHit = search.AttachmentSearchHit
type BacklinkEntry = search.BacklinkEntry

const attachmentMaxExtractedBytes = search.AttachmentMaxExtractedBytes

var (
	BuildIndex                = search.BuildIndex
	OpenIndex                 = search.OpenIndex
	errSearchBusy             = search.ErrBusy
	extractedAttachmentPath   = search.ExtractedAttachmentPath
	parseAttachmentPath       = search.ParseAttachmentPath
	decodeExtractedAttachment = search.DecodeExtractedAttachment
	encodeExtractedAttachment = search.EncodeExtractedAttachment
	attachmentBlobHash        = search.AttachmentBlobHash
)

func NewDocumentSearch(cfg Config) (*DocumentSearch, error) {
	return search.NewDocumentSearch(search.DocumentOptions{
		TikaURL:  cfg.TikaURL,
		ModelDir: cfg.DocumentSearch.ModelDir,
		Model:    cfg.DocumentSearch.Model,
	})
}
