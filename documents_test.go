package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search"
)

func TestChunkTextNormalisesAndOverlaps(t *testing.T) {
	words := make([]string, 200)
	for i := range words {
		words[i] = "word-" + string(rune('a'+i%26))
	}
	text := strings.Join(words, " ")
	text = "\r\n" + text[:len(text)/2] + "\x00" + text[len(text)/2:] + "\r"

	chunks := chunkText(text)
	if len(chunks) != 2 {
		t.Fatalf("chunkText returned %d chunks, want 2", len(chunks))
	}
	if chunks[0].Position != 0 || chunks[1].Position != 1 {
		t.Fatalf("chunk positions = %d, %d; want 0, 1", chunks[0].Position, chunks[1].Position)
	}
	if got := len(strings.Fields(chunks[0].Text)); got != 160 {
		t.Fatalf("first chunk has %d words, want 160", got)
	}
	secondWords := strings.Fields(chunks[1].Text)
	if len(secondWords) != 72 || secondWords[0] != words[128] {
		gotFirst := ""
		if len(secondWords) > 0 {
			gotFirst = secondWords[0]
		}
		t.Fatalf("second chunk starts with %q and has %d words, want %q and 72", gotFirst, len(secondWords), words[128])
	}
}

func TestParseAttachmentPath(t *testing.T) {
	owner, filename, ok := parseAttachmentPath("attachments/notes/reports/quarterly.PDF")
	if !ok || owner != "notes/reports" || filename != "quarterly.PDF" {
		t.Fatalf("parseAttachmentPath valid path = %q, %q, %v", owner, filename, ok)
	}
	for _, path := range []string{
		"attachments/notes/report/.quarterly.pdf",
		"attachments/notes/quarterly.pdf",
		"notes/report/quarterly.pdf",
	} {
		if _, _, ok := parseAttachmentPath(path); ok {
			t.Errorf("parseAttachmentPath(%q) accepted invalid path", path)
		}
	}
}

func TestExtractedAttachmentSidecar(t *testing.T) {
	path := "attachments/notes/reports/quarterly.pdf"
	if got, want := extractedAttachmentPath(path), "attachments/notes/reports/.hmd/extracted/quarterly.pdf.txt"; got != want {
		t.Fatalf("extractedAttachmentPath = %q, want %q", got, want)
	}
	hash := attachmentBlobHash([]byte("source"))
	text, ok := decodeExtractedAttachment(hash, encodeExtractedAttachment(hash, "extracted text"))
	if !ok || text != "extracted text" {
		t.Fatalf("decodeExtractedAttachment = %q, %v", text, ok)
	}
	if _, ok := decodeExtractedAttachment("stale", encodeExtractedAttachment(hash, "extracted text")); ok {
		t.Fatal("decodeExtractedAttachment accepted a stale sidecar")
	}
}

func TestTikaClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tika":
			_, _ = io.WriteString(w, "This is Tika Server")
		case r.Method == http.MethodPut && r.URL.Path == "/tika":
			if r.Header.Get("Accept") != "text/plain" || r.Header.Get("X-Tika-Skip-Embedded") != "true" {
				t.Errorf("Tika request headers = %q, %q", r.Header.Get("Accept"), r.Header.Get("X-Tika-Skip-Embedded"))
			}
			_, _ = io.WriteString(w, " extracted text ")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewTikaClient(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	text, err := client.Extract(context.Background(), strings.NewReader("document"), "report.pdf")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if text != "extracted text" {
		t.Fatalf("Extract returned %q, want %q", text, "extracted text")
	}
}

func TestExtractAttachmentTextBypassesTikaForUTF8Text(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "Tika should not be called", http.StatusInternalServerError)
	}))
	defer server.Close()

	tika, err := NewTikaClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	text, err := extractAttachmentText(context.Background(), tika, strings.NewReader(" note\r\ntext "), "source.md")
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if called || text != "note\ntext" {
		t.Fatalf("extractAttachmentText = %q, called Tika = %v", text, called)
	}
}

func TestEnsureEmbeddingModelDownloadsVerifiedFiles(t *testing.T) {
	contents := map[string]string{"config.json": "config", "onnx/model.onnx": "model"}
	files := make([]embeddingModelFile, 0, len(contents))
	for path, content := range contents {
		hash := sha256.Sum256([]byte(content))
		files = append(files, embeddingModelFile{Path: path, Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, ok := contents[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, content)
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := ensureEmbeddingModel(context.Background(), dir, server.URL, files); err != nil {
		t.Fatalf("ensureEmbeddingModel: %v", err)
	}
	for path, want := range contents {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Errorf("downloaded %q = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestOpenIndexRebuildsPageMaps(t *testing.T) {
	page := Page{Slug: "notes/home", Title: "Home", Tags: []string{"wiki"}, Body: "see [[Guide]]"}
	ix, err := openIndexAt(t.TempDir()+"/search.bleve", []Page{page}, map[string]string{page.Slug: "hash"}, nil, nil)
	if err != nil {
		t.Fatalf("openIndexAt: %v", err)
	}
	defer closeTestBody(t, ix.bleve)

	if slug, ok := ix.ResolveLink("Home", "notes"); !ok || slug != page.Slug {
		t.Fatalf("ResolveLink after open = %q, %v", slug, ok)
	}
	if got := ix.TagsFor(page.Slug); len(got) != 1 || got[0] != "wiki" {
		t.Fatalf("TagsFor after open = %v", got)
	}
	if got, err := ix.Search("Home"); err != nil || len(got) != 1 || got[0].Slug != page.Slug {
		t.Fatalf("Search after open = %v, %v", got, err)
	}
}

func testVector(position int) []float32 {
	vector := make([]float32, semanticModelDimensions)
	vector[position] = 1
	return vector
}

func TestCosineSimilarityAndTopSemanticChunks(t *testing.T) {
	if score, ok := cosineSimilarity(testVector(0), testVector(0)); !ok || score != 1 {
		t.Fatalf("cosineSimilarity identical vectors = %v, %v; want 1, true", score, ok)
	}
	if _, ok := cosineSimilarity(testVector(0), []float32{1}); ok {
		t.Fatal("cosineSimilarity accepted a malformed vector")
	}
	invalid := testVector(0)
	invalid[1] = float32(math.NaN())
	if _, ok := cosineSimilarity(testVector(0), invalid); ok {
		t.Fatal("cosineSimilarity accepted a non-finite vector")
	}
	manifest := newSearchManifest("test")
	manifest.Attachments["attachments/notes/a.pdf"] = attachmentManifest{
		ChunkIDs: []string{"b", "a"},
		Vectors:  [][]float32{testVector(0), testVector(0)},
	}
	manifest.Attachments["attachments/notes/b.pdf"] = attachmentManifest{
		ChunkIDs: []string{"bad"},
		Vectors:  [][]float32{{1}},
	}
	hits := topSemanticChunks(manifest, testVector(0), 1)
	if len(hits) != 1 || hits[0].id != "a" {
		t.Fatalf("topSemanticChunks = %#v; want stable top hit a", hits)
	}
}

func TestReplaceAttachmentPersistsAndRemovesVectors(t *testing.T) {
	idx, err := bleve.NewMemOnly(attachmentIndexMapping())
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, idx)
	ix := newIndex(idx)
	ix.manifest = newSearchManifest("test")
	ix.manifestPath = filepath.Join(t.TempDir(), "search.manifest.json")
	chunks := []textChunk{{Position: 0, Text: "first"}}
	if err := ix.replaceAttachment("attachments/notes/page/file.pdf", "notes/page", "file.pdf", "hash", chunks, [][]float32{testVector(0)}, nil); err != nil {
		t.Fatalf("replaceAttachment: %v", err)
	}
	entry := ix.manifest.Attachments["attachments/notes/page/file.pdf"]
	if !validAttachmentVectors(entry) {
		t.Fatalf("stored manifest vectors = %#v", entry)
	}
	if err := ix.replaceAttachment("attachments/notes/page/file.pdf", "notes/page", "file.pdf", "hash", nil, nil, errors.New("Tika failed")); err == nil {
		t.Fatal("replaceAttachment failure returned nil")
	}
	entry = ix.manifest.Attachments["attachments/notes/page/file.pdf"]
	if len(entry.ChunkIDs) != 0 || len(entry.Vectors) != 0 {
		t.Fatalf("failed replacement retained derived vectors: %#v", entry)
	}
	if err := ix.removeAttachment("attachments/notes/page/file.pdf"); err != nil {
		t.Fatalf("removeAttachment: %v", err)
	}
	if _, ok := ix.manifest.Attachments["attachments/notes/page/file.pdf"]; ok {
		t.Fatal("removeAttachment retained manifest entry")
	}
}

func TestFuseAttachmentResultsIncludesKeywordAndSemanticMatches(t *testing.T) {
	match := func(id, text string) *search.DocumentMatch {
		return &search.DocumentMatch{ID: id, Fields: map[string]interface{}{
			"OwnerSlug": "notes/page", "Filename": id + ".pdf", "AttachmentPath": "attachments/notes/page/" + id + ".pdf", "Text": text,
		}}
	}
	semantic := []semanticChunk{{id: "semantic", score: 1}}
	hits := fuseAttachmentResults([]*search.DocumentMatch{match("keyword", "keyword")}, semantic, map[string]*search.DocumentMatch{"semantic": match("semantic", "meaning")}, 20)
	if len(hits) != 2 || hits[0].Filename != "keyword.pdf" || hits[1].Filename != "semantic.pdf" {
		t.Fatalf("fused hits = %#v; want keyword then semantic", hits)
	}
}

type testEmbedder struct {
	vector []float32
	calls  int
}

func (e *testEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = e.vector
	}
	return vectors, nil
}

func (*testEmbedder) Revision() string { return "test" }

func TestOpenIndexReusesStoredAttachmentVectors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "search.bleve")
	idx, err := bleve.New(path, attachmentIndexMapping())
	if err != nil {
		t.Fatal(err)
	}
	ix := newIndex(idx)
	ix.indexDir = path
	ix.manifestPath = manifestPath(path)
	ix.manifest = newSearchManifest("test")
	ix.documents = &DocumentSearch{embedder: &testEmbedder{vector: testVector(0)}}
	if err := ix.replaceAttachment("attachments/notes/page/file.pdf", "notes/page", "file.pdf", "hash", []textChunk{{Text: "stored semantic text"}}, [][]float32{testVector(0)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	embedder := &testEmbedder{vector: testVector(0)}
	reopened, err := openIndexAt(path, nil, nil, map[string]string{"attachments/notes/page/file.pdf": "hash"}, &DocumentSearch{embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, reopened.bleve)
	if embedder.calls != 0 {
		t.Fatalf("reopening re-embedded %d times", embedder.calls)
	}
	hits, err := reopened.SearchAttachments(context.Background(), "unrelated", 20)
	if err != nil || len(hits) != 1 || hits[0].Filename != "file.pdf" {
		t.Fatalf("semantic search after reopen = %#v, %v", hits, err)
	}
}

func TestOldManifestSchemaRebuilds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.bleve")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeSearchManifest(manifestPath(path), searchManifest{SchemaVersion: 1, Pages: map[string]string{}, Attachments: map[string]attachmentManifest{}}); err != nil {
		t.Fatal(err)
	}
	ix, err := openIndexAt(path, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, ix.bleve)
	if ix.manifest.SchemaVersion != documentIndexSchemaVersion {
		t.Fatalf("schema version = %d, want %d", ix.manifest.SchemaVersion, documentIndexSchemaVersion)
	}
}
