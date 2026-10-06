// Package oauthserver contains HMD's MCP OAuth authorisation-server policy.
package oauthserver

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	errInvalidScope    = errors.New("invalid scope")
	errInvalidResource = errors.New("invalid resource")
	errInvalidPKCE     = errors.New("invalid PKCE value")
)

var validScopes = []string{"read", "settings", "write"}

func scopeSubset(candidate, allowed []string) bool {
	allowedSet := make(map[string]bool, len(allowed))
	for _, item := range expandSettings(allowed) {
		allowedSet[item] = true
	}
	for _, item := range candidate {
		if !allowedSet[item] {
			return false
		}
	}
	return true
}

func validateNamespaceSelection(selected, available []string) ([]string, error) {
	set := make(map[string]bool, len(available))
	for _, name := range available {
		set[name] = true
	}
	seen := make(map[string]bool, len(selected))
	out := make([]string, 0, len(selected))
	for _, name := range selected {
		if !set[name] || seen[name] {
			return nil, &ProtocolError{Code: "invalid_request", Description: "namespace selection is stale or invalid", Status: http.StatusBadRequest}
		}
		seen[name] = true
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

// NormaliseScopes validates and returns the supported action scopes in stable
// order. The settings scope remains distinct: HMD treats it as an unrestricted
// administrator grant rather than a namespace-limited action.
func NormaliseScopes(scopes []string) ([]string, error) {
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if !slices.Contains(validScopes, scope) {
			return nil, fmt.Errorf("%w: %q", errInvalidScope, scope)
		}
		seen[scope] = true
	}
	out := make([]string, 0, len(seen))
	for _, scope := range validScopes {
		if seen[scope] {
			out = append(out, scope)
		}
	}
	return out, nil
}

// EffectiveScopes intersects issued, consented, current-user and current-client
// permissions. Settings semantically implies read and write, matching HMD's
// existing permission model.
func EffectiveScopes(issued, consented, user, client []string) []string {
	i := expandSettings(issued)
	c := expandSettings(consented)
	u := expandSettings(user)
	p := expandSettings(client)
	allowed := make(map[string]bool, len(i))
	for _, scope := range i {
		allowed[scope] = true
	}
	for _, set := range [][]string{c, u, p} {
		next := make(map[string]bool, len(set))
		for _, scope := range set {
			if allowed[scope] {
				next[scope] = true
			}
		}
		allowed = next
	}
	// Administrator authority is retained only when every bound authorisation
	// independently includes settings.
	settings := slices.Contains(issued, "settings") &&
		slices.Contains(consented, "settings") &&
		slices.Contains(user, "settings") &&
		slices.Contains(client, "settings")
	var out []string
	for _, scope := range []string{"read", "write"} {
		if allowed[scope] {
			out = append(out, scope)
		}
	}
	if settings {
		out = append(out, "settings")
	}
	return out
}

func expandSettings(scopes []string) []string {
	out, _ := NormaliseScopes(scopes)
	if slices.Contains(out, "settings") {
		out = append(out, "read", "write")
		slices.Sort(out)
		out = slices.Compact(out)
	}
	return out
}

// ValidateS256Challenge enforces the unpadded base64url encoding of a
// 32-byte SHA-256 digest required by PKCE S256.
func ValidateS256Challenge(challenge string) error {
	if len(challenge) != 43 {
		return errInvalidPKCE
	}
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil || len(decoded) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(decoded) != challenge {
		return errInvalidPKCE
	}
	return nil
}

// ValidatePKCEVerifier checks the RFC 7636 syntax and binds it to an S256
// challenge.
func ValidatePKCEVerifier(verifier, challenge string) error {
	if len(verifier) < 43 || len(verifier) > 128 || !utf8.ValidString(verifier) {
		return errInvalidPKCE
	}
	for _, r := range verifier {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') &&
			(r < '0' || r > '9') && !strings.ContainsRune("-._~", r) {
			return errInvalidPKCE
		}
	}
	if err := ValidateS256Challenge(challenge); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
		return errInvalidPKCE
	}
	return nil
}

// ValidateResource requires exactly one absolute HTTP(S) resource URL without
// user-info, query or fragment and requires an exact match to the configured
// resource. It intentionally does not broaden paths or normalise trailing
// slashes.
func ValidateResource(values []string, configured string, required bool) error {
	if len(values) == 0 && !required {
		return nil
	}
	if len(values) != 1 || values[0] != configured {
		return errInvalidResource
	}
	u, err := url.Parse(values[0])
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && u.Scheme != "http") {
		return errInvalidResource
	}
	return nil
}
