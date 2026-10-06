package httpmiddleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hmd/internal/auth"
	"hmd/internal/config"
)

func openTestAuth(t testing.TB) *auth.Auth {
	t.Helper()
	a, err := auth.Open(auth.Options{AppDir: t.TempDir(), AdminUser: "admin", AdminPass: "password12345"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCompression(t *testing.T) {
	body := strings.Repeat("compress me ", 100)
	handler := Compression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil || string(decoded) != body {
		t.Fatalf("decoded response = %q, %v", decoded, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("disabled gzip Content-Encoding = %q", got)
	}
}

func TestOAuthCredentialResponsesAreNotCompressed(t *testing.T) {
	handler := Compression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "synthetic credentials and client-controlled text")
	}))
	for _, path := range []string{"/_/oauth/token", "/_/oauth/authorize", "/_/oauth/register", "/_/admin/oauth"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("credential response at %s was compressed", path)
		}
	}
}

func TestRecoveryReturnsGenericErrorAndRequestID(t *testing.T) {
	handler := AccessLog(RecoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
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

func TestRequestSecurity(t *testing.T) {
	authn := openTestAuth(t)
	session, ok := authn.Login("admin", "password12345")
	if !ok {
		t.Fatal("login failed")
	}
	token := authn.CSRFToken(session)
	cfg := config.Config{BaseURL: "https://wiki.example.com", TrustedProxies: []string{"10.0.0.0/8"}}
	security := Security{Config: func() config.Config { return cfg }, Auth: authn}
	called := 0
	handler := security.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))

	request := func(origin, csrf string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "https://wiki.example.com/notes/page?do=save", strings.NewReader("csrf_token="+csrf))
		r.Host = "wiki.example.com"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "hmd_session", Value: session})
		return r
	}
	for _, tc := range []struct{ origin, token string }{{"https://evil.example", token}, {"https://wiki.example.com", "wrong"}, {"", token}} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(tc.origin, tc.token))
		if rec.Code != http.StatusForbidden {
			t.Errorf("origin=%q token=%q status=%d, want 403", tc.origin, tc.token, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request("https://wiki.example.com", token))
	if rec.Code != http.StatusOK || called != 1 {
		t.Fatalf("valid CSRF status/calls = %d/%d, want 200/1", rec.Code, called)
	}

	req := httptest.NewRequest(http.MethodGet, "http://wiki.example.com/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-Proto", "https")
	if IsSecureRequest(req, cfg) {
		t.Fatal("untrusted forwarded proto marked request secure")
	}
	if !SecureCookie(req, cfg) {
		t.Fatal("HTTPS base_url did not enforce Secure cookies")
	}
	req.RemoteAddr = "10.1.2.3:1234"
	if !IsSecureRequest(req, cfg) {
		t.Fatal("trusted forwarded proto did not mark request secure")
	}
	plain := config.Config{BaseURL: "http://wiki.example.com"}
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Del("X-Forwarded-Proto")
	if SecureCookie(req, plain) {
		t.Fatal("plain HTTP deployment unexpectedly enforced Secure cookies")
	}
}

func TestRequestSecurityRejectsCrossOriginLogin(t *testing.T) {
	authn := openTestAuth(t)
	cfg := config.Config{BaseURL: "https://wiki.example.com"}
	security := Security{Config: func() config.Config { return cfg }, Auth: authn}
	called := 0
	handler := security.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	authn := openTestAuth(f)
	session, ok := authn.Login("admin", "password12345")
	if !ok {
		f.Fatal("login failed")
	}
	token := authn.CSRFToken(session)
	cfg := config.Config{BaseURL: "https://wiki.example.com"}
	security := Security{Config: func() config.Config { return cfg }, Auth: authn}
	handler := security.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
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
