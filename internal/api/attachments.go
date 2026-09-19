package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"hmd/internal/search"
	"hmd/internal/wiki"
)

// UploadCapabilityTTL bounds how long a capability upload URL stays usable.
const UploadCapabilityTTL = 10 * time.Minute

// CanonicalAttachmentFilename reduces a caller-supplied filename to the single
// attachment filename shape HMD stores: the base name only, its stem slugified
// and its extension lower-cased. It is the one filename rule shared by MCP
// capability issuing and the HTTP upload transport, so neither can diverge.
func CanonicalAttachmentFilename(input string) (string, error) {
	filename := filepath.Base(input)
	ext := filepath.Ext(filename)
	name := wiki.Slugify(filename[:len(filename)-len(ext)])
	if name == "" {
		return "", errors.New("filename is not allowed")
	}
	return name + strings.ToLower(ext), nil
}

// UploadGrant is a one-use attachment upload permission bound to an owning
// page, a canonical filename and the issuing actor.
type UploadGrant struct {
	Slug     string
	Filename string
	Token    string
	Expires  time.Time
}

// IssueUploadCapability canonicalises filename and mints a bounded, one-use
// capability bound to the caller, the owning page and the canonical filename.
// It never grants unrestricted write access: redeeming the capability only
// uploads that one filename to that one page.
func (a *API) IssueUploadCapability(ctx context.Context, slug, filename string) (*UploadGrant, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	canonical, err := CanonicalAttachmentFilename(filename)
	if err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, Unavailable("creating upload URL", err)
	}
	actor, _ := Username(ctx)
	expires := time.Now().Add(UploadCapabilityTTL)
	tokenString := fmt.Sprintf("%x", token)
	if err := a.addUploadCapability(tokenString, UploadCapability{Slug: slug, Filename: canonical, User: actor, Expires: expires}); err != nil {
		return nil, Busy(err.Error())
	}
	return &UploadGrant{Slug: slug, Filename: canonical, Token: tokenString, Expires: expires}, nil
}

// AttachmentUploadInput describes one authenticated attachment upload.
// Identity always comes from the context, never a submitted actor field.
type AttachmentUploadInput struct {
	Slug     string
	Filename string
	Content  []byte
}

// AttachmentUpload is a committed attachment. Indexed reports whether the
// derived document index holds the current revision; IndexWarning is non-empty
// when the commit is durable but indexing is pending.
type AttachmentUpload struct {
	Slug         string
	Filename     string
	Indexed      bool
	IndexWarning string
}

// UploadAttachment checks the caller's write access, then uses the same
// canonicalisation and atomic file/sidecar commit pipeline as capability
// redemption. Storage and extraction rules cannot diverge between transports.
func (a *API) UploadAttachment(ctx context.Context, in AttachmentUploadInput) (*AttachmentUpload, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !AllowSlug(ctx, in.Slug) {
		return nil, Forbidden("namespace access denied")
	}
	actor, _ := Username(ctx)
	return a.uploadAttachment(ctx, in, "", actor)
}

// RedeemUploadCapability consumes the server-issued capability before checking
// the filename. Its stored owner, filename and actor are authoritative; the
// unauthenticated transport cannot manufacture a principal or widen the grant.
func (a *API) RedeemUploadCapability(ctx context.Context, token, filename string, content []byte) (*AttachmentUpload, error) {
	capability, ok := a.takeUploadCapability(token)
	if !ok || time.Now().After(capability.Expires) {
		return nil, NotFound("upload URL not found or expired")
	}
	return a.uploadAttachment(ctx, AttachmentUploadInput{
		Slug: capability.Slug, Filename: filename, Content: content,
	}, capability.Filename, capability.User)
}

func (a *API) uploadAttachment(ctx context.Context, in AttachmentUploadInput, expectedFilename, actor string) (*AttachmentUpload, error) {
	if !wiki.ValidPageSlug(in.Slug) {
		return nil, InvalidInput("invalid slug", nil)
	}
	filename, err := CanonicalAttachmentFilename(in.Filename)
	if err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	if expectedFilename != "" && filename != expectedFilename {
		return nil, InvalidInput("filename does not match upload URL", nil)
	}
	path := "attachments/" + in.Slug + "/" + filename
	if !a.attachmentPathAllowed(path) {
		return nil, InvalidInput("invalid slug", nil)
	}
	files := map[string][]byte{path: in.Content}
	if a.index != nil {
		if extracted, ok := a.index.ExtractAttachment(ctx, bytes.NewReader(in.Content), filename, in.Content); ok {
			files[search.ExtractedAttachmentPath(path)] = extracted
		}
	}
	name, email := a.authorFor(actor)
	if _, err := a.store.SaveAll(files, "Add attachment "+filename, name, email); err != nil {
		return nil, Unavailable("saving attachment", err)
	}
	upload := &AttachmentUpload{Slug: in.Slug, Filename: filename}
	if a.index != nil && a.index.DocumentsEnabled() {
		file, hash, openErr := a.store.OpenAttachment(path)
		if openErr != nil {
			upload.IndexWarning = "attachment indexing pending"
			slog.Warn("attachment indexing failed", "path", path, "err", openErr)
		} else {
			if closeErr := file.Close(); closeErr != nil {
				slog.Warn("closing indexed attachment", "path", path, "err", closeErr)
			}
			if indexErr := a.index.ReconcileAttachmentPath(path, hash); indexErr != nil {
				upload.IndexWarning = "attachment indexing pending"
				slog.Warn("attachment indexing failed", "path", path, "err", indexErr)
			} else {
				upload.Indexed = a.index.AttachmentIndexed(path, hash)
			}
		}
	}
	slog.Info("attachment uploaded", "slug", in.Slug, "filename", filename, "author", name)
	return upload, nil
}

// OpenAttachmentForRead opens a page's attachment for streaming to a reader.
// The slug must be a valid page, the caller must hold access to its namespace,
// and an anonymous caller may only read an attachment on a public page. The
// returned file is positioned at the start and must be closed by the caller.
func (a *API) OpenAttachmentForRead(ctx context.Context, slug, filename string) (io.ReadSeekCloser, error) {
	if !wiki.ValidPageSlug(slug) {
		return nil, NotFound("attachment not found")
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	if _, ok := Username(ctx); !ok && !a.Namespaces().IsPublic(slug) {
		return nil, NotFound("attachment not found")
	}
	if err := a.requirePageRead(ctx, slug); err != nil {
		return nil, err
	}
	if filepath.Base(filename) != filename {
		return nil, NotFound("attachment not found")
	}
	path := "attachments/" + slug + "/" + filename
	if !a.attachmentPathAllowed(path) {
		return nil, NotFound("attachment not found")
	}
	file, _, err := a.store.OpenAttachment(path)
	if err != nil {
		return nil, NotFoundCause("attachment not found", err)
	}
	return file, nil
}

// attachmentPathAllowed verifies that a cleaned attachment path stays under the
// repository's attachments directory. The slug is a caller-supplied path
// segment, so it must not be able to escape the tree.
func (a *API) attachmentPathAllowed(path string) bool {
	if a.store == nil {
		return false
	}
	absRepo, err := filepath.Abs(filepath.Join(a.store.Dir(), "attachments"))
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(filepath.Join(a.store.Dir(), path))
	if err != nil {
		return false
	}
	return strings.HasPrefix(absPath, absRepo+string(filepath.Separator))
}

// ReadAttachment returns the cached extracted text of an attachment after
// verifying it still matches the current source blob; a stale extraction is
// rejected rather than returned.
func (a *API) ReadAttachment(ctx context.Context, slug, filename string) (string, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return "", err
	}
	if !wiki.ValidPageSlug(slug) {
		return "", InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, slug) {
		return "", Forbidden("namespace access denied")
	}
	if filepath.Base(filename) != filename {
		return "", InvalidInput("filename must not contain a path", nil)
	}
	path := "attachments/" + slug + "/" + filename
	if _, _, ok := search.ParseAttachmentPath(path); !ok {
		return "", InvalidInput("invalid attachment filename", nil)
	}
	source, hash, err := a.store.OpenAttachment(path)
	if err != nil {
		return "", NotFound("attachment not found")
	}
	if err := source.Close(); err != nil {
		slog.Warn("closing attachment", "path", path, "err", err)
	}
	sidecar, err := a.store.OpenExtractedAttachment(path)
	if err != nil {
		return "", NotFound("attachment has no cached extraction")
	}
	content, err := io.ReadAll(io.LimitReader(sidecar, search.AttachmentMaxExtractedBytes+256))
	closeErr := sidecar.Close()
	if err != nil {
		return "", Unavailable("reading extracted attachment", err)
	}
	if closeErr != nil {
		return "", Unavailable("closing extracted attachment", closeErr)
	}
	text, ok := search.DecodeExtractedAttachment(hash, content)
	if !ok {
		return "", InvalidInput("attachment extraction is stale", nil)
	}
	return text, nil
}
