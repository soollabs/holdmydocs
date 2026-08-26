package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeEndpointsAreUnauthenticated(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	for _, path := range []string{"/_/live", "/_/ready"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	if !app.Index.Ready() {
		t.Fatal("test index should be ready")
	}
	if err := app.Index.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(server.URL + "/_/ready")
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness after index close = %d, want 503", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/_/live")
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("liveness after index close = %d, want 200", response.StatusCode)
	}
}

func TestRecoveryReturnsGenericErrorAndRequestID(t *testing.T) {
	handler := accessLog(recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret panic")
	})))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?token=secret", nil))
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "internal error" {
		t.Fatalf("recovery response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
}

func TestRequestSecurityRejectsCrossOriginLogin(t *testing.T) {
	app := &App{}
	app.SetConfig(Config{BaseURL: "https://wiki.example.com"})
	called := 0
	handler := app.requestSecurity(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name          string
		origin        string
		secFetchSite  string
		wantStatus    int
		wantForwarded bool
	}{
		{name: "same origin", origin: "https://wiki.example.com", wantStatus: http.StatusNoContent, wantForwarded: true},
		{name: "foreign origin", origin: "https://evil.example", wantStatus: http.StatusForbidden},
		{name: "null origin", origin: "null", wantStatus: http.StatusForbidden},
		{name: "cross-site fetch metadata", secFetchSite: "cross-site", wantStatus: http.StatusForbidden},
		{name: "non-browser client", wantStatus: http.StatusNoContent, wantForwarded: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := called
			request := httptest.NewRequest(http.MethodPost, "https://wiki.example.com/_/login", strings.NewReader("username=admin"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			if tc.secFetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", tc.secFetchSite)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tc.wantStatus)
			}
			if forwarded := called == before+1; forwarded != tc.wantForwarded {
				t.Fatalf("forwarded = %t, want %t", forwarded, tc.wantForwarded)
			}
		})
	}
}

func FuzzRequestSecurityOrigin(f *testing.F) {
	auth, err := OpenAuth(Config{AppDir: f.TempDir(), AdminUser: "admin", AdminPass: "password12345"})
	if err != nil {
		f.Fatal(err)
	}
	session, ok := auth.Login("admin", "password12345")
	if !ok {
		f.Fatal("login failed")
	}
	token := auth.CSRFToken(session)
	app := &App{Auth: auth}
	app.SetConfig(Config{BaseURL: "https://wiki.example.com"})
	handler := app.requestSecurity(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, origin := range []string{"", "https://wiki.example.com", "https://wiki.example.com.evil", "https://wiki.example.com/", "null"} {
		f.Add(origin)
	}
	f.Fuzz(func(t *testing.T, origin string) {
		request := httptest.NewRequest(http.MethodPost, "https://wiki.example.com/notes/page?do=save", strings.NewReader("csrf_token="+token))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", origin)
		request.AddCookie(&http.Cookie{Name: "hmd_session", Value: session})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusForbidden
		if origin == "https://wiki.example.com" {
			want = http.StatusNoContent
		}
		if response.Code != want {
			t.Fatalf("origin %q status = %d, want %d", origin, response.Code, want)
		}
	})
}
