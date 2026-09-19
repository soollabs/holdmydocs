package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"hmd/internal/auth"
	"hmd/internal/search"
	"hmd/internal/store"
)

func newAttachmentAPI(t *testing.T) (*API, *store.Store) {
	t.Helper()
	st, err := store.Open(store.Options{RepoDir: t.TempDir(), Git: store.GitOptions{User: "tester"}})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ix, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return New(st, ix, nil), st
}

func TestCanonicalAttachmentFilename(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "Report.PDF", want: "report.pdf"},
		{in: "my file.txt", want: "my-file.txt"},
		{in: "../../etc/passwd", want: "passwd"},
		{in: "!!!.png", wantErr: true},
		{in: ".hidden", wantErr: true},
	} {
		got, err := CanonicalAttachmentFilename(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("CanonicalAttachmentFilename(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("CanonicalAttachmentFilename(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

// TestIssueUploadCapabilityBindsOwnerFilenameAndActor verifies a capability is
// not unrestricted write access: it carries the owning page, the canonical
// filename and the issuing actor.
func TestIssueUploadCapabilityBindsOwnerFilenameAndActor(t *testing.T) {
	a, _ := newAttachmentAPI(t)
	grant, err := a.IssueUploadCapability(writeCtx(), "notes/page", "Report.PDF")
	if err != nil {
		t.Fatalf("IssueUploadCapability: %v", err)
	}
	if grant.Filename != "report.pdf" || grant.Slug != "notes/page" || grant.Token == "" {
		t.Fatalf("grant = %+v", grant)
	}
	capability, ok := a.takeUploadCapability(grant.Token)
	if !ok {
		t.Fatal("issued capability was not stored")
	}
	if capability.Slug != "notes/page" || capability.Filename != "report.pdf" || capability.User != "tester" {
		t.Fatalf("stored capability = %+v", capability)
	}
	if _, ok := a.takeUploadCapability(grant.Token); ok {
		t.Fatal("capability was reusable; it must be one-use")
	}
}

func TestUploadAttachmentRejectsMismatchedCapabilityFilename(t *testing.T) {
	a, _ := newAttachmentAPI(t)
	grant, err := a.IssueUploadCapability(writeCtx(), "notes/page", "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.RedeemUploadCapability(context.Background(), grant.Token, "other.txt", []byte("x"))
	if err == nil || CategoryOf(err) != CategoryInvalidInput {
		t.Fatalf("mismatched upload = %v, want invalid input", err)
	}
}

func TestUploadCapabilitiesAreBoundedAndExpiredEntriesPurged(t *testing.T) {
	client := New(nil, nil, nil)
	past := time.Now().Add(-time.Second)
	future := time.Now().Add(time.Minute)
	if err := client.addUploadCapability("stale", UploadCapability{Expires: past}); err != nil {
		t.Fatal(err)
	}
	for i := range MaxPendingUploadCapabilities {
		if err := client.addUploadCapability(fmt.Sprintf("token-%d", i), UploadCapability{Expires: future}); err != nil {
			t.Fatalf("adding capability %d: %v (stale entry was not purged)", i, err)
		}
	}
	if err := client.addUploadCapability("overflow", UploadCapability{Expires: future}); err == nil {
		t.Fatal("pending upload capability limit was not enforced")
	}
	if _, ok := client.takeUploadCapability("token-0"); !ok {
		t.Fatal("taking a capability failed")
	}
}

// TestUploadAttachmentCommitsAndReadsExtraction exercises the single upload
// pipeline end to end: canonicalisation, commit, extraction sidecar and cached
// text validation.
func TestUploadAttachmentCommitsAndReadsExtraction(t *testing.T) {
	a, st := newAttachmentAPI(t)
	upload, err := a.UploadAttachment(writeCtx(), AttachmentUploadInput{
		Slug: "notes/page", Filename: "Source.TXT", Content: []byte("source text"),
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if upload.Filename != "source.txt" || upload.Slug != "notes/page" {
		t.Fatalf("upload = %+v", upload)
	}
	if _, _, err := st.Read("attachments/notes/page/source.txt"); err != nil {
		t.Fatalf("committed attachment missing: %v", err)
	}
	text, err := a.ReadAttachment(writeCtx(), "notes/page", "source.txt")
	if err != nil || text != "source text" {
		t.Fatalf("ReadAttachment = %q, %v; want %q", text, err, "source text")
	}
}

func TestReadAttachmentDeniesNamespace(t *testing.T) {
	a, _ := newAttachmentAPI(t)
	ctx := auth.WithTokenPrincipal(context.Background(), auth.TokenPrincipal{
		User: "reader", Scopes: []string{"read"}, Namespaces: []string{"public"},
	})
	_, err := a.ReadAttachment(ctx, "private/page", "source.txt")
	if err == nil || CategoryOf(err) != CategoryForbidden {
		t.Fatalf("ReadAttachment on a denied namespace = %v, want forbidden", err)
	}
}
