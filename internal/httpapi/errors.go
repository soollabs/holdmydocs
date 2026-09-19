package httpapi

import (
	"net/http"

	"hmd/internal/api"
)

// statusForCategory maps an application error category onto the HTTP status a
// browser data endpoint returns. Adapters map categories, never internal
// causes, so an unexpected dependency failure cannot leak implementation
// detail to the browser.
func statusForCategory(category api.Category) int {
	switch category {
	case api.CategoryInvalidInput:
		return http.StatusBadRequest
	case api.CategoryUnauthenticated:
		return http.StatusUnauthorized
	case api.CategoryForbidden:
		return http.StatusForbidden
	case api.CategoryNotFound:
		return http.StatusNotFound
	case api.CategoryConflict:
		return http.StatusConflict
	case api.CategoryBusy:
		return http.StatusTooManyRequests
	case api.CategoryUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// writeAPIError encodes a categorised application failure as JSON, mapping the
// category to an HTTP status so the browser can distinguish outcomes without
// parsing prose. A busy result carries Retry-After so the client can back off.
func writeAPIError(w http.ResponseWriter, err error) {
	category := api.CategoryOf(err)
	if category == api.CategoryBusy {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, statusForCategory(category), map[string]string{"error": err.Error()})
}

// writeNamespaceDenied is the JSON response for a request whose principal may
// not act in the target namespace.
func writeNamespaceDenied(w http.ResponseWriter) {
	writeAPIError(w, api.Forbidden("namespace access denied"))
}
