package testhttp

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRequestsUseTestContextAndPreservePayload(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, "form"} {
		t.Run(method, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Context() != t.Context() {
					t.Fatal("request does not use the test context")
				}
				wantMethod := method
				switch method {
				case "form":
					wantMethod = http.MethodPost
					if err := request.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if request.PostForm.Get("name") != "value with spaces" {
						t.Fatal("form encoding changed")
					}
				case http.MethodPost:
					body, err := io.ReadAll(request.Body)
					if err != nil || string(body) != "payload" || request.Header.Get("Content-Type") != "text/plain" {
						t.Fatalf("POST payload changed: %s (%v)", body, err)
					}
				}
				if request.Method != wantMethod {
					t.Fatalf("method = %s, want %s", request.Method, wantMethod)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("reply")), Header: make(http.Header)}, nil
			})}
			var response *http.Response
			var err error
			switch method {
			case http.MethodGet:
				response, err = Get(t, client, "https://wiki.example")
			case http.MethodPost:
				response, err = Post(t, client, "https://wiki.example", "text/plain", strings.NewReader("payload"))
			default:
				response, err = PostForm(t, client, "https://wiki.example", url.Values{"name": {"value with spaces"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
