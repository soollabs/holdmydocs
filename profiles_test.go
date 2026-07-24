package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

// TestProfileNoStorageEffect renders every page under all five profiles and
// asserts the repo's git HEAD is unchanged — switching profile is a
// presentation change only, never a commit.
func TestProfileNoStorageEffect(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	headBefore := gitHead(t, app.config().RepoDir)

	for _, name := range profileNames {
		form := url.Values{"profile": {name}}
		req, _ := http.NewRequest("POST", server.URL+"/settings/appearance", bytes.NewBufferString(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /settings/appearance (profile=%s) failed: %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("profile=%s: status = %d, want 303", name, resp.StatusCode)
		}

		resp2, err := client.Get(server.URL + "/page/readme")
		if err != nil {
			t.Fatalf("GET /page/readme (profile=%s) failed: %v", name, err)
		}
		body, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("profile=%s: GET /page/readme status = %d, body: %s", name, resp2.StatusCode, body)
		}
	}

	headAfter := gitHead(t, app.config().RepoDir)
	if headBefore != headAfter {
		t.Errorf("switching profile and rendering pages moved HEAD: %s -> %s", headBefore, headAfter)
	}
}

func gitHead(t *testing.T, repoDir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}
