package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/wiki"
)

// uploadAttachment streams a multipart upload to the page named in the route.
func (h *Handlers) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !api.AllowSlug(r.Context(), slug) {
		writeNamespaceDenied(w)
		return
	}
	h.upload(w, r, slug, "")
}

// capabilityUpload redeems a one-use upload capability and streams its file to
// the owning page.
func (h *Handlers) capabilityUpload(w http.ResponseWriter, r *http.Request) {
	h.upload(w, r, "", r.PathValue("token"))
}

// upload parses the multipart body and enforces the transport upload bound,
// then streams the file to the shared application upload operation. Filename
// canonicalisation, storage, extraction and capability binding live behind
// api.UploadAttachment so the MCP and browser transports cannot diverge.
func (h *Handlers) upload(w http.ResponseWriter, r *http.Request, slug, token string) {
	if token == "" && !wiki.ValidPageSlug(slug) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}

	maxBytes := h.api.Config().MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	var file *os.File
	filenameInput := ""
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](partErr); ok {
				http.Error(w, "attachment exceeds maximum upload size", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" || file != nil {
			if err := part.Close(); err != nil {
				http.Error(w, "error reading upload", http.StatusInternalServerError)
				return
			}
			continue
		}
		file, err = os.CreateTemp("", "hmd-upload-*")
		if err != nil {
			http.Error(w, "error preparing upload", http.StatusInternalServerError)
			return
		}
		filenameInput = part.FileName()
		copied, copyErr := io.Copy(file, io.LimitReader(part, maxBytes+1))
		closeErr := part.Close()
		if copyErr != nil || closeErr != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
			http.Error(w, "error reading file", http.StatusInternalServerError)
			return
		}
		if copied > maxBytes {
			_ = file.Close()
			_ = os.Remove(file.Name())
			http.Error(w, "attachment exceeds maximum upload size", http.StatusRequestEntityTooLarge)
			return
		}
	}
	if file == nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Warn("closing uploaded file", "err", err)
		}
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("removing upload temporary file", "err", err)
		}
	}()

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "error reading file", http.StatusInternalServerError)
		return
	}
	content, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "error reading file", http.StatusInternalServerError)
		return
	}

	var upload *api.AttachmentUpload
	if token != "" {
		upload, err = h.api.RedeemUploadCapability(r.Context(), token, filenameInput, content)
	} else {
		upload, err = h.api.UploadAttachment(r.Context(), api.AttachmentUploadInput{
			Slug: slug, Filename: filenameInput, Content: content,
		})
	}
	if err != nil {
		switch api.CategoryOf(err) {
		case api.CategoryNotFound:
			http.NotFound(w, r)
		case api.CategoryUnauthenticated:
			http.Error(w, "authentication required", http.StatusUnauthorized)
		case api.CategoryForbidden:
			http.Error(w, "forbidden", http.StatusForbidden)
		case api.CategoryInvalidInput:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, "error saving file", http.StatusInternalServerError)
		}
		return
	}

	// The Git commit is already durable even when the disposable derived index
	// cannot be rebuilt, so an index warning still answers with a body.
	w.Header().Set("Content-Type", "application/json")
	if upload.IndexWarning != "" {
		w.WriteHeader(http.StatusAccepted)
	} else if h.documents {
		w.WriteHeader(http.StatusCreated)
	}
	resp := map[string]any{
		"url":     fmt.Sprintf("/_/attachments/%s/%s", upload.Slug, upload.Filename),
		"indexed": upload.Indexed,
	}
	if upload.IndexWarning != "" {
		resp["index_error"] = upload.IndexWarning
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding attachment response", "err", err)
	}
}

// serveAttachment streams a stored attachment with a conservative content type
// and disposition.
func (h *Handlers) serveAttachment(w http.ResponseWriter, r *http.Request) {
	// {path...} is "{slug}/{file}"; slug itself may contain "/" for a
	// namespaced page, so only the last segment is ever the filename.
	path := r.PathValue("path")
	if strings.Contains(path, "/.hmd/") {
		http.NotFound(w, r)
		return
	}
	i := strings.LastIndex(path, "/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	slug, file := path[:i], path[i+1:]

	attachment, err := h.api.OpenAttachmentForRead(r.Context(), slug, file)
	if err != nil {
		if api.CategoryOf(err) == api.CategoryForbidden {
			http.Error(w, "403 Forbidden: namespace access denied", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
		return
	}
	defer func() {
		if err := attachment.Close(); err != nil {
			slog.Warn("closing served attachment", "path", path, "err", err)
		}
	}()

	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch strings.ToLower(filepath.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
	default:
		w.Header().Set("Content-Disposition", "attachment")
	}
	http.ServeContent(w, r, file, time.Time{}, attachment)
}
