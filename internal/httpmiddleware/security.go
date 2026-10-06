package httpmiddleware

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hmd/internal/auth"
	"hmd/internal/config"
)

// MaxFormBytes bounds a non-multipart form body read by request security.
const MaxFormBytes = 2 << 20

type cspNonceKey struct{}

// CspNonce returns the per-request script/style nonce set by SecurityHeaders,
// or the empty string when the request did not pass through it.
func CspNonce(ctx context.Context) string {
	nonce, _ := ctx.Value(cspNonceKey{}).(string)
	return nonce
}

// SecurityHeaders stamps the baseline security headers, including a per-request
// CSP nonce, before the request reaches any handler.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(b)
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")

		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'; img-src 'self' data:; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'nonce-"+nonce+"' 'unsafe-inline'; worker-src 'self' blob:")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceKey{}, nonce)))
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	contentType := w.Header().Get("Content-Type")
	if status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified &&
		w.Header().Get("Content-Encoding") == "" &&
		(strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") || strings.Contains(contentType, "javascript")) {
		w.Header().Del("Content-Length")
		w.Header().Set("Content-Encoding", "gzip")
		w.writer = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.writer != nil {
		return w.writer.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func acceptsGzip(header string) bool {
	for value := range strings.SplitSeq(header, ",") {
		parts := strings.Split(strings.TrimSpace(value), ";")
		if parts[0] != "gzip" && parts[0] != "*" {
			continue
		}
		quality := 1.0
		for _, param := range parts[1:] {
			if raw, ok := strings.CutPrefix(strings.TrimSpace(param), "q="); ok {
				quality, _ = strconv.ParseFloat(raw, 64)
			}
		}
		return quality > 0
	}
	return false
}

// Compression gzips compressible text responses when the client accepts gzip.
// It never compresses HEAD, range requests or the MCP streaming endpoint.
func Compression(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || r.URL.Path == "/_/mcp" ||
			strings.HasPrefix(r.URL.Path, "/_/oauth/") || r.URL.Path == "/_/admin/oauth" ||
			!acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		compressed := &gzipResponseWriter{ResponseWriter: w}
		next.ServeHTTP(compressed, r)
		if compressed.writer != nil {
			_ = compressed.writer.Close()
		}
	})
}

// Security enforces request-body bounds, HSTS and CSRF/origin checks for
// state-changing browser requests. It reads the live configuration and the auth
// store, which are application primitives rather than adapter types.
type Security struct {
	Config func() config.Config
	Auth   *auth.Auth
}

// IsSecureRequest reports whether a request arrived over TLS, honouring
// X-Forwarded-Proto only from a configured trusted proxy.
func IsSecureRequest(r *http.Request, cfg config.Config) bool {
	if r.TLS != nil {
		return true
	}
	if !trustedProxy(r.RemoteAddr, cfg) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

// SecureCookie reports whether session cookies should carry the Secure flag:
// the request is secure, or the deployment declares an https base URL.
func SecureCookie(r *http.Request, cfg config.Config) bool {
	if IsSecureRequest(r, cfg) {
		return true
	}
	base, err := url.Parse(cfg.BaseURL)
	return err == nil && strings.EqualFold(base.Scheme, "https")
}

func trustedProxy(remote string, cfg config.Config) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range cfg.TrustedProxies {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// RequestOrigin returns the scheme and host the browser used, preferring a
// configured base URL, for CSRF origin comparison.
func RequestOrigin(r *http.Request, cfg config.Config) string {
	if cfg.BaseURL != "" {
		return cfg.BaseURL
	}
	scheme := "http"
	if IsSecureRequest(r, cfg) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// Handler applies request security: it caps non-upload bodies, adds HSTS on
// secure requests, and enforces CSRF/origin checks on cookie-authenticated
// state-changing requests. Bearer-authenticated and capability-upload requests
// are exempt, as are safe methods.
func (s Security) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.Config()
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !strings.HasPrefix(r.URL.Path, "/_/api/attachments/") && !strings.HasPrefix(r.URL.Path, "/_/api/attachment-uploads/") && !strings.HasPrefix(r.URL.Path, "/_/mcp") {
			if r.ContentLength > MaxFormBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, MaxFormBytes)
		}
		if IsSecureRequest(r, cfg) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || strings.HasPrefix(r.URL.Path, "/_/api/attachment-uploads/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/_/oauth/token" || r.URL.Path == "/_/oauth/revoke" || r.URL.Path == "/_/oauth/register" {
			// Protocol endpoints use OAuth client authentication; browser cookies
			// are not credentials for these requests.
			next.ServeHTTP(w, r)
			return
		}
		if _, bearer := auth.TokenPrincipalFromContext(r.Context()); bearer {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/_/login" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != RequestOrigin(r, cfg) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if origin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		cookie, err := r.Cookie("hmd_session")
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" || origin != RequestOrigin(r, cfg) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			if err := r.ParseForm(); err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			token = r.Form.Get("csrf_token")
		}
		expected := s.Auth.CSRFToken(cookie.Value)
		if expected == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
