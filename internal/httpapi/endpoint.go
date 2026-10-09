package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"hmd/internal/api"
)

// maxJSONBodyBytes bounds a JSON request body before decoding. Ordinary data
// endpoints carry small inputs; it matches the shared request-security form
// bound so a full-length page body plus its metadata still fits. Multipart and
// preview bodies have their own concrete bounds.
const maxJSONBodyBytes = 2 << 20

// registerJSON registers an ordinary JSON data endpoint. It centralises bounded
// input decoding, api.Error category mapping and JSON encoding, so a concrete
// handler supplies only its typed operation call.
//
// Go 1.27 concrete generic methods let one helper serve every ordinary endpoint
// while each operation keeps an explicit input and result type. Multipart,
// streaming, authentication and preview handlers deliberately do not use it.
func (h *Handlers) registerJSON[In any, Out any](mux *http.ServeMux, pattern string, op func(context.Context, In) (Out, error)) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var in In
		if err := decodeInput(w, r, &in); err != nil {
			writeAPIError(w, err)
			return
		}
		out, err := op(r.Context(), in)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// registerJSONPath registers an ordinary JSON endpoint whose operation also
// needs the first path wildcard value (the page slug). It shares registerJSON's
// bounded decoding, category mapping and encoding; only the slug threading
// differs, which keeps concrete page operations explicit.
func (h *Handlers) registerJSONPath[In any, Out any](mux *http.ServeMux, pattern string, op func(context.Context, string, In) (Out, error)) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var in In
		if err := decodeInput(w, r, &in); err != nil {
			writeAPIError(w, err)
			return
		}
		out, err := op(r.Context(), r.PathValue("slug"), in)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// decodeInput fills in from a bounded JSON request body, or, for a bodyless GET,
// from the URL query using each field's json tag as the parameter name.
func decodeInput[In any](w http.ResponseWriter, r *http.Request, in *In) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return decodeQuery(r, in)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return api.InvalidInput("request body too large", err)
		}
		return api.InvalidInput("invalid JSON request body", err)
	}
	// A successful first Decode does not consume trailing input. Require EOF
	// so concatenated values, junk and oversized trailing whitespace cannot
	// slip past validation or the body bound before an operation runs.
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return api.InvalidInput("request body must contain a single JSON value", err)
	}
	return nil
}

// decodeQuery fills a string-valued input struct from the URL query. Query
// inputs are flat and string-typed, so a small reflection pass over json tags
// keeps each endpoint's input type declared once.
func decodeQuery[In any](r *http.Request, in *In) error {
	values := r.URL.Query()
	v := reflect.ValueOf(in).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Type.Kind() != reflect.String {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			name = field.Name
		}
		v.Field(i).SetString(values.Get(name))
	}
	return nil
}

// writeJSON encodes out as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, out any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(out); err != nil {
		slog.Error("encoding JSON response", "err", err)
	}
}
