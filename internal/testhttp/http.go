// Package testhttp provides context-aware HTTP requests for integration tests.
package testhttp

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Get sends a GET request bound to the test's lifetime.
// The caller owns the response body.
func Get(t testing.TB, client *http.Client, target string) (*http.Response, error) {
	t.Helper()
	return send(t, client, http.MethodGet, target, "", nil)
}

// Post sends a POST request bound to the test's lifetime.
// The caller owns the response body.
func Post(t testing.TB, client *http.Client, target, contentType string, body io.Reader) (*http.Response, error) {
	t.Helper()
	return send(t, client, http.MethodPost, target, contentType, body)
}

// PostForm sends a form request bound to the test's lifetime.
// The caller owns the response body.
func PostForm(t testing.TB, client *http.Client, target string, values url.Values) (*http.Response, error) {
	t.Helper()
	return Post(t, client, target, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
}

func send(t testing.TB, client *http.Client, method, target, contentType string, body io.Reader) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, target, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return client.Do(request)
}
