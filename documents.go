package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

const (
	attachmentMaxExtractedBytes = 8 << 20
	attachmentExtractTimeout    = 60 * time.Second
	semanticModelName           = "BAAI/bge-small-en-v1.5"
	semanticModelRevision       = "5c38ec7c405ec4b44b94cc5a9bb96e735b38267a"
	semanticModelDimensions     = 384
	documentIndexSchemaVersion  = 2
	attachmentPipelineName      = "hmd-document-search"
	extractedAttachmentDir      = ".hmd/extracted"
	extractedAttachmentPrefix   = "<!-- hmd:source-hash "
)

var errDocumentSearchDisabled = errors.New("attachment search is disabled")

type embeddingModelFile struct {
	Path   string
	Size   int64
	SHA256 string
}

type embeddingModelSpec struct {
	Revision string
	Files    []embeddingModelFile
}

var embeddingModelFiles = []embeddingModelFile{
	{"config.json", 743, "094f8e891b932f2000c92cfc663bac4c62069f5d8af5b5278c4306aef3084750"},
	{"tokenizer.json", 711396, "d241a60d5e8f04cc1b2b3e9ef7a4921b27bf526d9f6050ab90f9267a1f9e5c66"},
	{"tokenizer_config.json", 366, "9261e7d79b44c8195c1cada2b453e55b00aeb81e907a6664974b4d7776172ab3"},
	{"special_tokens_map.json", 125, "b6d346be366a7d1d48332dbc9fdf3bf8960b5d879522b7799ddba59e76237ee3"},
	{"vocab.txt", 231508, "07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3"},
	{"onnx/model.onnx", 133093490, "828e1496d7fabb79cfa4dcd84fa38625c0d3d21da474a00f08db0f559940cf35"},
}

var genericEmbeddingModelFiles = []embeddingModelFile{
	{Path: "config.json"},
	{Path: "tokenizer.json"},
	{Path: "tokenizer_config.json"},
	{Path: "special_tokens_map.json"},
	{Path: "vocab.txt"},
	{Path: "onnx/model.onnx"},
}

func embeddingModelFor(name string) (embeddingModelSpec, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(name, "\\ ") || strings.Contains(name, "..") {
		return embeddingModelSpec{}, fmt.Errorf("invalid document search model %q: want Hugging Face owner/model", name)
	}
	if name == semanticModelName {
		return embeddingModelSpec{Revision: semanticModelRevision, Files: embeddingModelFiles}, nil
	}
	return embeddingModelSpec{Revision: "main", Files: genericEmbeddingModelFiles}, nil
}

type attachmentSource struct {
	Path string
	Hash string
}

type AttachmentSearchHit struct {
	OwnerSlug string  `json:"owner_slug"`
	Filename  string  `json:"filename"`
	URL       string  `json:"url"`
	Excerpt   string  `json:"excerpt"`
	Score     float64 `json:"score"`
}

type textChunk struct {
	Position int
	Text     string
}

// normaliseExtractedText makes Tika output deterministic before it reaches the
// tokenizer. strings.Fields below then gives all platforms the same chunks.
func normaliseExtractedText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\x00", "")
	return strings.TrimSpace(text)
}

func chunkText(text string) []textChunk {
	words := strings.Fields(normaliseExtractedText(text))
	if len(words) == 0 {
		return nil
	}
	const chunkWords = 160
	const overlapWords = 32
	var chunks []textChunk
	for position, start := 0, 0; start < len(words); position++ {
		end := start + chunkWords
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, textChunk{Position: position, Text: strings.Join(words[start:end], " ")})
		if end == len(words) {
			break
		}
		start = end - overlapWords
	}
	return chunks
}

func attachmentChunkID(path string, position int) string {
	hash := sha256.Sum256([]byte(path))
	return fmt.Sprintf("attachment:%s:%06d", hex.EncodeToString(hash[:]), position)
}

func parseAttachmentPath(path string) (owner, filename string, ok bool) {
	path = filepath.ToSlash(path)
	prefix := attachmentsDir + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	i := strings.LastIndexByte(rest, '/')
	if i < 1 || i == len(rest)-1 {
		return "", "", false
	}
	owner, filename = rest[:i], rest[i+1:]
	if !isPageSlug(owner) || strings.ContainsAny(filename, `/\\`) || strings.HasPrefix(filename, ".") {
		return "", "", false
	}
	return owner, filename, true
}

func extractedAttachmentPath(path string) string {
	owner, filename, ok := parseAttachmentPath(path)
	if !ok {
		return ""
	}
	return attachmentsDir + "/" + owner + "/" + extractedAttachmentDir + "/" + filename + ".txt"
}

func encodeExtractedAttachment(sourceHash, text string) []byte {
	return []byte(extractedAttachmentPrefix + sourceHash + " -->\n" + text)
}

func decodeExtractedAttachment(sourceHash string, content []byte) (string, bool) {
	prefix := extractedAttachmentPrefix + sourceHash + " -->\n"
	if !strings.HasPrefix(string(content), prefix) {
		return "", false
	}
	return strings.TrimPrefix(string(content), prefix), true
}

func attachmentBlobHash(content []byte) string {
	return plumbing.ComputeHash(plumbing.BlobObject, content).String()
}

func directTextAttachment(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".txt":
		return true
	default:
		return false
	}
}

// TikaClient is deliberately just net/http. Parser isolation, OCR and resource
// limits belong to the operator-owned Tika deployment, not to HMD.
type TikaClient struct {
	baseURL string
	client  *http.Client
	sem     chan struct{}
}

func NewTikaClient(raw string) (*TikaClient, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("HMD_TIKA_URL must be an http or https URL with a host and no user-info")
	}
	return &TikaClient{baseURL: raw, client: &http.Client{}, sem: make(chan struct{}, 1)}, nil
}

func (c *TikaClient) Validate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/tika", nil)
	if err != nil {
		return fmt.Errorf("creating Tika validation request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("validating Tika server: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if readErr != nil {
		return fmt.Errorf("reading Tika validation response: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "This is Tika Server") {
		return fmt.Errorf("Tika validation failed: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *TikaClient) Extract(ctx context.Context, body io.ReadSeeker, filename string) (string, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewinding attachment: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentExtractTimeout)
	defer cancel()
	filename = filepath.Base(filename)
	filename = strings.NewReplacer("\r", "", "\n", "", "\"", "'", ";", "_").Replace(filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/tika", body)
	if err != nil {
		return "", fmt.Errorf("creating Tika extraction request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	req.Header.Set("X-Tika-Skip-Embedded", "true")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("extracting attachment with Tika: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("Tika extraction failed: HTTP %d: %q", resp.StatusCode, string(body))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, attachmentMaxExtractedBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading Tika extraction: %w", err)
	}
	if len(data) > attachmentMaxExtractedBytes {
		return "", fmt.Errorf("Tika extraction exceeded %d bytes", attachmentMaxExtractedBytes)
	}
	return normaliseExtractedText(string(data)), nil
}

func extractAttachmentText(ctx context.Context, tika *TikaClient, body io.ReadSeeker, filename string) (string, error) {
	if directTextAttachment(filename) {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("rewinding attachment: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(body, attachmentMaxExtractedBytes+1))
		if err != nil {
			return "", fmt.Errorf("reading attachment: %w", err)
		}
		if len(data) > attachmentMaxExtractedBytes {
			return "", fmt.Errorf("attachment text exceeded %d bytes", attachmentMaxExtractedBytes)
		}
		if !utf8.Valid(data) {
			return "", errors.New("attachment text is not valid UTF-8")
		}
		return normaliseExtractedText(string(data)), nil
	}
	return tika.Extract(ctx, body, filename)
}

type HugotEmbedder struct {
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	revision string
}

func ensureEmbeddingModel(ctx context.Context, modelDir, baseURL string, files []embeddingModelFile) error {
	// An existing ONNX file makes model_dir an escape hatch for operator-managed models.
	if _, err := os.Stat(filepath.Join(modelDir, "onnx", "model.onnx")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking embedding model: %w", err)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	for _, file := range files {
		destination := filepath.Join(modelDir, filepath.FromSlash(file.Path))
		if info, err := os.Stat(destination); err == nil && (file.Size == 0 || info.Size() == file.Size) {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checking embedding model file %q: %w", file.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return fmt.Errorf("creating embedding model directory: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/"+file.Path, nil)
		if err != nil {
			return fmt.Errorf("creating embedding model download request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("downloading embedding model file %q: %w", file.Path, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("downloading embedding model file %q: HTTP %d", file.Path, resp.StatusCode)
		}
		tmp, err := os.CreateTemp(filepath.Dir(destination), ".hmd-model-*")
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("creating embedding model file %q: %w", file.Path, err)
		}
		body := io.Reader(resp.Body)
		if file.Size > 0 {
			body = io.LimitReader(resp.Body, file.Size+1)
		}
		n, copyErr := io.Copy(tmp, body)
		closeErr := resp.Body.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil && file.Size > 0 && n != file.Size {
			copyErr = fmt.Errorf("got %d bytes, want %d", n, file.Size)
		}
		if copyErr == nil {
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				copyErr = err
			} else {
				hash := sha256.New()
				_, copyErr = io.Copy(hash, tmp)
				if copyErr == nil && file.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
					copyErr = errors.New("checksum mismatch")
				}
			}
		}
		if err := tmp.Close(); copyErr == nil {
			copyErr = err
		}
		if copyErr != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("downloading embedding model file %q: %w", file.Path, copyErr)
		}
		if err := os.Rename(tmp.Name(), destination); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("installing embedding model file %q: %w", file.Path, err)
		}
	}
	return nil
}

func NewHugotEmbedder(modelDir, model string) (*HugotEmbedder, error) {
	spec, err := embeddingModelFor(model)
	if err != nil {
		return nil, err
	}
	baseURL := "https://huggingface.co/" + model + "/resolve/" + spec.Revision
	if err := ensureEmbeddingModel(context.Background(), modelDir, baseURL, spec.Files); err != nil {
		return nil, err
	}
	session, err := hugot.NewGoSession(context.Background())
	if err != nil {
		return nil, fmt.Errorf("creating Hugot session: %w", err)
	}
	pipeline, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{
		ModelPath:    modelDir,
		Name:         attachmentPipelineName,
		OnnxFilename: "model.onnx",
		Options:      []hugot.FeatureExtractionOption{pipelines.WithNormalization()},
	})
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("loading embedding model: %w", err)
	}
	embedder := &HugotEmbedder{session: session, pipeline: pipeline, revision: model + "@" + spec.Revision}
	probe, err := embedder.Embed(context.Background(), []string{"hmd probe"})
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("probing embedding model: %w", err)
	}
	if len(probe) != 1 || len(probe[0]) != semanticModelDimensions {
		_ = session.Destroy()
		return nil, fmt.Errorf("embedding model returned %d dimensions, want %d", len(probe[0]), semanticModelDimensions)
	}
	return embedder, nil
}

func (e *HugotEmbedder) Revision() string { return e.revision }

func (e *HugotEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var result [][]float32
	for start := 0; start < len(texts); start += 32 {
		end := start + 32
		if end > len(texts) {
			end = len(texts)
		}
		output, err := e.pipeline.RunPipeline(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, output.Embeddings...)
	}
	return result, nil
}

type documentEmbedder interface {
	Embed(context.Context, []string) ([][]float32, error)
	Revision() string
}

type DocumentSearch struct {
	tika     *TikaClient
	embedder documentEmbedder
	store    *Store
}

func NewDocumentSearch(cfg Config) (*DocumentSearch, error) {
	tika, err := NewTikaClient(cfg.TikaURL)
	if err != nil {
		return nil, err
	}
	if err := tika.Validate(context.Background()); err != nil {
		return nil, err
	}
	embedder, err := NewHugotEmbedder(cfg.DocumentSearch.ModelDir, cfg.DocumentSearch.Model)
	if err != nil {
		return nil, err
	}
	return &DocumentSearch{tika: tika, embedder: embedder}, nil
}

type attachmentManifest struct {
	SourceHash string      `json:"source_hash"`
	ChunkIDs   []string    `json:"chunk_ids"`
	Vectors    [][]float32 `json:"vectors"`
}

type searchManifest struct {
	SchemaVersion int                           `json:"schema_version"`
	ModelRevision string                        `json:"model_revision"`
	Pages         map[string]string             `json:"pages"`
	Attachments   map[string]attachmentManifest `json:"attachments"`
}

func newSearchManifest(revision string) searchManifest {
	return searchManifest{
		SchemaVersion: documentIndexSchemaVersion,
		ModelRevision: revision,
		Pages:         make(map[string]string),
		Attachments:   make(map[string]attachmentManifest),
	}
}

func pageDocument(p Page) map[string]interface{} {
	return map[string]interface{}{"Type": "page", "Title": p.Title, "Body": p.Body, "Tags": p.Tags}
}

func attachmentDocument(source attachmentSource, owner, filename string, chunk textChunk) map[string]interface{} {
	return map[string]interface{}{
		"Type": "attachment", "Text": chunk.Text,
		"OwnerSlug": owner, "AttachmentPath": source.Path, "Filename": filename,
		"SourceHash": source.Hash, "Position": chunk.Position,
	}
}

func attachmentIndexMapping() *mapping.IndexMappingImpl {
	mapping := bleve.NewIndexMapping()
	mapping.TypeField = "Type"
	page := bleve.NewDocumentStaticMapping()
	for _, field := range []string{"Type", "Title", "Body", "Tags"} {
		fm := bleve.NewTextFieldMapping()
		if field == "Type" {
			fm = bleve.NewKeywordFieldMapping()
		}
		page.AddFieldMappingsAt(field, fm)
	}
	attachment := bleve.NewDocumentStaticMapping()
	typeField := bleve.NewKeywordFieldMapping()
	attachment.AddFieldMappingsAt("Type", typeField)
	textField := bleve.NewTextFieldMapping()
	textField.Store = true
	attachment.AddFieldMappingsAt("Text", textField)
	for _, field := range []string{"OwnerSlug", "AttachmentPath", "Filename", "SourceHash"} {
		fm := bleve.NewKeywordFieldMapping()
		fm.Store = true
		attachment.AddFieldMappingsAt(field, fm)
	}
	position := bleve.NewNumericFieldMapping()
	position.Store = true
	attachment.AddFieldMappingsAt("Position", position)
	mapping.AddDocumentMapping("page", page)
	mapping.AddDocumentMapping("attachment", attachment)
	mapping.DefaultMapping = bleve.NewDocumentDisabledMapping()
	return mapping
}

func manifestPath(indexDir string) string { return indexDir + ".manifest.json" }

func loadSearchManifest(path string) (searchManifest, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return searchManifest{}, false, nil
	}
	if err != nil {
		return searchManifest{}, true, err
	}
	var manifest searchManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return searchManifest{}, true, err
	}
	if manifest.SchemaVersion != documentIndexSchemaVersion || manifest.Pages == nil || manifest.Attachments == nil {
		return searchManifest{}, true, fmt.Errorf("unsupported search manifest")
	}
	return manifest, true, nil
}

func writeSearchManifest(path string, manifest searchManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeDerivedSearchState(indexDir string) error {
	if err := os.RemoveAll(indexDir); err != nil {
		return err
	}
	if err := os.Remove(manifestPath(indexDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// OpenIndex opens the one persistent Bleve index and reconciles its disposable
// state against Git-backed page and attachment hashes.
func OpenIndex(appDir string, pages []Page, pageHashes map[string]string, attachments map[string]string, documents *DocumentSearch) (*Index, error) {
	return openIndexAt(filepath.Join(appDir, "search.bleve"), pages, pageHashes, attachments, documents)
}

func openIndexAt(indexDir string, pages []Page, pageHashes map[string]string, attachments map[string]string, documents *DocumentSearch) (*Index, error) {
	appDir := filepath.Dir(indexDir)
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return nil, fmt.Errorf("creating search directory: %w", err)
	}
	revision := ""
	if documents != nil {
		revision = documents.embedder.Revision()
	}
	manifestFile := manifestPath(indexDir)
	manifest, hasManifest, manifestErr := loadSearchManifest(manifestFile)
	if manifestErr != nil || (hasManifest && manifest.ModelRevision != revision) {
		if err := removeDerivedSearchState(indexDir); err != nil {
			return nil, fmt.Errorf("removing derived search state: %w", err)
		}
		hasManifest = false
	}
	mapping := attachmentIndexMapping()
	var idx bleve.Index
	var err error
	if hasManifest {
		idx, err = bleve.Open(indexDir)
		if err != nil {
			if err := removeDerivedSearchState(indexDir); err != nil {
				return nil, fmt.Errorf("resetting corrupt search index: %w", err)
			}
			hasManifest = false
		}
	} else if _, statErr := os.Stat(indexDir); statErr == nil {
		if err := removeDerivedSearchState(indexDir); err != nil {
			return nil, err
		}
	}
	if !hasManifest {
		idx, err = bleve.New(indexDir, mapping)
		if err != nil {
			return nil, fmt.Errorf("creating search index: %w", err)
		}
		manifest = newSearchManifest(revision)
	}
	ix := newIndex(idx)
	ix.indexDir = indexDir
	ix.manifestPath = manifestFile
	ix.manifest = manifest
	ix.documents = documents
	for _, page := range pages {
		ix.titles[page.Slug] = page.Title
		ix.pinned[page.Slug] = page.Pin
		ix.pageTags[page.Slug] = page.Tags
		ix.forward[page.Slug] = WikiLinks(page.Body)
		for _, tag := range page.Tags {
			tagSlug := Slugify(tag)
			if ix.tags[tagSlug] == nil {
				ix.tags[tagSlug] = make(map[string]bool)
			}
			ix.tags[tagSlug][page.Slug] = true
			ix.tagNames[tagSlug] = tag
		}
	}
	if err := ix.reconcilePages(pages, pageHashes); err != nil {
		_ = idx.Close()
		return nil, err
	}
	if documents != nil {
		for _, path := range sortedAttachmentPaths(attachments) {
			if err := ix.ReconcileAttachmentPath(path, attachments[path]); err != nil {
				slog.Warn("indexing attachment", "path", path, "err", err)
			}
		}
		for path := range manifest.Attachments {
			if _, ok := attachments[path]; !ok {
				if err := ix.removeAttachment(path); err != nil {
					slog.Warn("removing attachment from index", "path", path, "err", err)
				}
			}
		}
	}
	return ix, nil
}

func sortedAttachmentPaths(paths map[string]string) []string {
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func (ix *Index) reconcilePages(pages []Page, pageHashes map[string]string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	seen := make(map[string]bool, len(pages))
	batch := ix.bleve.NewBatch()
	changed := false
	for _, page := range pages {
		seen[page.Slug] = true
		hash := pageHashes[page.Slug]
		if ix.manifest.Pages[page.Slug] == hash {
			continue
		}
		batch.Delete(pageDocumentID(page.Slug))
		if err := batch.Index(pageDocumentID(page.Slug), pageDocument(page)); err != nil {
			return err
		}
		ix.manifest.Pages[page.Slug] = hash
		changed = true
	}
	for slug := range ix.manifest.Pages {
		if !seen[slug] {
			batch.Delete(pageDocumentID(slug))
			delete(ix.manifest.Pages, slug)
			changed = true
		}
	}
	if changed {
		if err := ix.bleve.Batch(batch); err != nil {
			return err
		}
	}
	return writeSearchManifest(ix.manifestPath, ix.manifest)
}

func (ix *Index) ReconcileAttachmentPath(path, expectedHash string) error {
	if ix.documents == nil {
		return errDocumentSearchDisabled
	}
	ix.mu.RLock()
	previous, exists := ix.manifest.Attachments[path]
	failedHash := ix.failedAttachments[path]
	ix.mu.RUnlock()
	if exists && previous.SourceHash == expectedHash && validAttachmentVectors(previous) {
		return nil
	}
	if failedHash == expectedHash {
		return nil
	}
	return ix.reconcileAttachmentPath(path, expectedHash)
}

func (ix *Index) reconcileAttachmentPath(path, expectedHash string) error {
	if ix.documents == nil {
		return errDocumentSearchDisabled
	}
	owner, filename, ok := parseAttachmentPath(path)
	if !ok {
		return fmt.Errorf("invalid attachment path %q", path)
	}
	if ix.documents.store == nil {
		return errors.New("attachment store is not configured")
	}
	hash := expectedHash
	text := ""
	if hash != "" {
		if sidecar, err := ix.documents.store.OpenExtractedAttachment(path); err == nil {
			data, readErr := io.ReadAll(io.LimitReader(sidecar, attachmentMaxExtractedBytes+256))
			closeErr := sidecar.Close()
			if readErr == nil && closeErr == nil {
				text, _ = decodeExtractedAttachment(hash, data)
			}
		}
	}
	if text == "" {
		body, actualHash, err := ix.documents.store.OpenAttachment(path)
		if err != nil {
			return err
		}
		defer body.Close()
		if hash != "" && actualHash != hash {
			return fmt.Errorf("attachment %q changed during reconciliation", path)
		}
		hash = actualHash
		text, err = extractAttachmentText(context.Background(), ix.documents.tika, body, filename)
		if err != nil {
			return ix.replaceAttachment(path, owner, filename, hash, nil, nil, err)
		}
	}
	chunks := chunkText(text)
	if len(chunks) == 0 {
		return ix.replaceAttachment(path, owner, filename, hash, nil, nil, errors.New("Tika extracted no text"))
	}
	texts := make([]string, len(chunks))
	for i := range chunks {
		texts[i] = chunks[i].Text
	}
	vectors, err := ix.documents.embedder.Embed(context.Background(), texts)
	if err != nil {
		return ix.replaceAttachment(path, owner, filename, hash, nil, nil, fmt.Errorf("embedding attachment: %w", err))
	}
	if len(vectors) != len(chunks) {
		return ix.replaceAttachment(path, owner, filename, hash, nil, nil, errors.New("embedding returned the wrong number of vectors"))
	}
	for _, vector := range vectors {
		if len(vector) != semanticModelDimensions {
			return ix.replaceAttachment(path, owner, filename, hash, nil, nil, fmt.Errorf("embedding returned %d dimensions, want %d", len(vector), semanticModelDimensions))
		}
	}
	return ix.replaceAttachment(path, owner, filename, hash, chunks, vectors, nil)
}

func validAttachmentVectors(entry attachmentManifest) bool {
	if len(entry.ChunkIDs) == 0 || len(entry.ChunkIDs) != len(entry.Vectors) {
		return false
	}
	for _, vector := range entry.Vectors {
		if len(vector) != semanticModelDimensions {
			return false
		}
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return false
			}
		}
	}
	return true
}

func (ix *Index) replaceAttachment(path, owner, filename, hash string, chunks []textChunk, vectors [][]float32, failure error) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	old := ix.manifest.Attachments[path]
	batch := ix.bleve.NewBatch()
	for _, id := range old.ChunkIDs {
		batch.Delete(id)
	}
	if failure != nil {
		if err := ix.bleve.Batch(batch); err != nil {
			return err
		}
		ix.manifest.Attachments[path] = attachmentManifest{SourceHash: hash}
		ix.failedAttachments[path] = hash
		if err := writeSearchManifest(ix.manifestPath, ix.manifest); err != nil {
			return err
		}
		return failure
	}
	if len(chunks) != len(vectors) {
		return errors.New("attachment reconciliation failed")
	}
	ids := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		id := attachmentChunkID(path, chunk.Position)
		if err := batch.Index(id, attachmentDocument(attachmentSource{Path: path, Hash: hash}, owner, filename, chunk)); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := ix.bleve.Batch(batch); err != nil {
		return err
	}
	ix.manifest.Attachments[path] = attachmentManifest{SourceHash: hash, ChunkIDs: ids, Vectors: vectors}
	delete(ix.failedAttachments, path)
	if err := writeSearchManifest(ix.manifestPath, ix.manifest); err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("attachment %q was not indexed", path)
	}
	return nil
}

func (ix *Index) removeAttachment(path string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	entry, ok := ix.manifest.Attachments[path]
	if !ok {
		return nil
	}
	batch := ix.bleve.NewBatch()
	for _, id := range entry.ChunkIDs {
		batch.Delete(id)
	}
	if err := ix.bleve.Batch(batch); err != nil {
		return err
	}
	delete(ix.manifest.Attachments, path)
	delete(ix.failedAttachments, path)
	return writeSearchManifest(ix.manifestPath, ix.manifest)
}

func (ix *Index) ReconcileAttachments(hashes map[string]string) {
	if ix.documents == nil {
		return
	}
	for _, path := range sortedAttachmentPaths(hashes) {
		if err := ix.ReconcileAttachmentPath(path, hashes[path]); err != nil {
			slog.Warn("reconciling attachment", "path", path, "err", err)
		}
	}
	ix.mu.RLock()
	var removed []string
	for path := range ix.manifest.Attachments {
		if _, ok := hashes[path]; !ok {
			removed = append(removed, path)
		}
	}
	ix.mu.RUnlock()
	for _, path := range removed {
		if err := ix.removeAttachment(path); err != nil {
			slog.Warn("removing attachment chunks", "path", path, "err", err)
		}
	}
}

func attachmentHitFromMatch(match *search.DocumentMatch) AttachmentSearchHit {
	value := func(name string) string {
		if value, ok := match.Fields[name]; ok {
			return fmt.Sprint(value)
		}
		return ""
	}
	owner, filename, path := value("OwnerSlug"), value("Filename"), value("AttachmentPath")
	excerpt := ""
	if fragments := match.Fragments["Text"]; len(fragments) > 0 {
		excerpt = fragments[0]
	} else {
		excerpt = html.EscapeString(runeExcerpt(value("Text"), 320))
	}
	return AttachmentSearchHit{
		OwnerSlug: owner,
		Filename:  filename,
		URL:       "/_/attachments/" + strings.TrimPrefix(path, attachmentsDir+"/"),
		Excerpt:   excerpt,
		Score:     match.Score,
	}
}

func runeExcerpt(text string, max int) string {
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "…"
}

func (ix *Index) SearchAttachments(ctx context.Context, query string, limit int) ([]AttachmentSearchHit, error) {
	if ix.documents == nil {
		return nil, errDocumentSearchDisabled
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("attachment search query cannot be empty")
	}
	if err := validateSearchQuery(query); err != nil {
		return nil, err
	}
	select {
	case ix.searches <- struct{}{}:
		defer func() { <-ix.searches }()
	default:
		return nil, errSearchBusy
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	vectors, err := ix.documents.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || len(vectors[0]) != semanticModelDimensions {
		return nil, errors.New("query embedding has the wrong dimensions")
	}
	typeQuery := bleve.NewTermQuery("attachment")
	typeQuery.SetField("Type")
	textQuery := bleve.NewMatchQuery(query)
	textQuery.SetField("Text")
	keyword := bleve.NewConjunctionQuery(textQuery, typeQuery)
	candidates := limit * 4
	if candidates < 50 {
		candidates = 50
	}
	request := bleve.NewSearchRequest(keyword)
	request.Size = candidates
	request.Fields = []string{"OwnerSlug", "AttachmentPath", "Filename", "SourceHash", "Position", "Text"}
	request.Highlight = bleve.NewHighlightWithStyle("html")
	request.Highlight.Fields = []string{"Text"}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	keywordResults, err := ix.bleve.Search(request)
	if err != nil {
		return nil, err
	}
	semantic := topSemanticChunks(ix.manifest, vectors[0], candidates)
	semanticMatches := make(map[string]*search.DocumentMatch, len(semantic))
	if len(semantic) > 0 {
		ids := make([]string, len(semantic))
		for i, candidate := range semantic {
			ids[i] = candidate.id
		}
		semanticRequest := bleve.NewSearchRequest(bleve.NewDocIDQuery(ids))
		semanticRequest.Fields = request.Fields
		results, err := ix.bleve.Search(semanticRequest)
		if err != nil {
			return nil, err
		}
		for _, match := range results.Hits {
			semanticMatches[match.ID] = match
		}
	}
	return fuseAttachmentResults(keywordResults.Hits, semantic, semanticMatches, limit), nil
}

type semanticChunk struct {
	id    string
	score float64
}

func cosineSimilarity(left, right []float32) (float64, bool) {
	if len(left) != semanticModelDimensions || len(right) != semanticModelDimensions {
		return 0, false
	}
	var dot, leftNorm, rightNorm float64
	for i := range left {
		if math.IsNaN(float64(left[i])) || math.IsInf(float64(left[i]), 0) || math.IsNaN(float64(right[i])) || math.IsInf(float64(right[i]), 0) {
			return 0, false
		}
		dot += float64(left[i] * right[i])
		leftNorm += float64(left[i] * left[i])
		rightNorm += float64(right[i] * right[i])
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0, false
	}
	return dot / (leftNorm * rightNorm), true
}

func topSemanticChunks(manifest searchManifest, query []float32, limit int) []semanticChunk {
	var result []semanticChunk
	for _, entry := range manifest.Attachments {
		if !validAttachmentVectors(entry) {
			continue
		}
		for i, vector := range entry.Vectors {
			score, ok := cosineSimilarity(query, vector)
			if !ok {
				continue
			}
			candidate := semanticChunk{id: entry.ChunkIDs[i], score: score}
			at := sort.Search(len(result), func(i int) bool {
				return result[i].score < candidate.score || (result[i].score == candidate.score && result[i].id > candidate.id)
			})
			if at == limit {
				continue
			}
			result = append(result, semanticChunk{})
			copy(result[at+1:], result[at:])
			result[at] = candidate
			if len(result) > limit {
				result = result[:limit]
			}
		}
	}
	return result
}

func fuseAttachmentResults(keyword []*search.DocumentMatch, semantic []semanticChunk, semanticMatches map[string]*search.DocumentMatch, limit int) []AttachmentSearchHit {
	const reciprocalRankOffset = 60
	type fusedHit struct {
		hit   AttachmentSearchHit
		score float64
		id    string
	}
	results := make(map[string]*fusedHit, len(keyword)+len(semantic))
	add := func(id string, rank int, match *search.DocumentMatch) {
		if match == nil {
			return
		}
		result := results[id]
		if result == nil {
			result = &fusedHit{hit: attachmentHitFromMatch(match), id: id}
			results[id] = result
		}
		result.score += 1 / float64(reciprocalRankOffset+rank)
		result.hit.Score = result.score
	}
	for rank, match := range keyword {
		add(match.ID, rank+1, match)
	}
	for rank, candidate := range semantic {
		add(candidate.id, rank+1, semanticMatches[candidate.id])
	}
	fused := make([]*fusedHit, 0, len(results))
	for _, result := range results {
		fused = append(fused, result)
	}
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].score > fused[j].score || (fused[i].score == fused[j].score && fused[i].id < fused[j].id)
	})
	if len(fused) > limit {
		fused = fused[:limit]
	}
	hits := make([]AttachmentSearchHit, len(fused))
	for i, result := range fused {
		hits[i] = result.hit
	}
	return hits
}
