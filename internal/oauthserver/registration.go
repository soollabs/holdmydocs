package oauthserver

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
)

type registrationRequest struct {
	Name          string   `json:"client_name"`
	RedirectURIs  []string `json:"redirect_uris"`
	AuthMethod    string   `json:"token_endpoint_auth_method"`
	Scope         string   `json:"scope"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
}

// RegistrationHandler implements bounded, opt-in RFC 7591 registration.
// Registration never creates a user grant or bypasses PKCE and consent.
func (s *Service) RegistrationHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		if !s.options.DynamicRegistration {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.CheckRateLimit(w, r) || !s.checkRegistrationRate(w) {
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "application/json is required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var input registrationRequest
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&input); err != nil {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid registration document")
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "expected one registration document")
			return
		}
		for _, grant := range input.GrantTypes {
			if grant != "authorization_code" && grant != "refresh_token" {
				writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant type")
				return
			}
		}
		if len(input.GrantTypes) > 0 && !slices.Contains(input.GrantTypes, "authorization_code") {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "authorization_code is required")
			return
		}
		if len(input.ResponseTypes) > 0 && (len(input.ResponseTypes) != 1 || input.ResponseTypes[0] != "code") {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only the code response type is supported")
			return
		}
		if input.Name == "" {
			input.Name = "Dynamically registered application"
		}
		if input.AuthMethod == "" {
			input.AuthMethod = clientAuthBasic
		}
		if input.Scope == "" {
			// MCP clients such as ChatGPT may omit scope during dynamic client
			// registration, then request read and write at authorization time.
			// Both remain subject to explicit user consent; settings is never
			// available to dynamically registered clients.
			input.Scope = "read write"
		}
		scopes := strings.Fields(input.Scope)
		if slices.Contains(scopes, "settings") {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "dynamic clients cannot request administrator access")
			return
		}
		for _, redirect := range input.RedirectURIs {
			if err := validateRedirectURI(redirect); err != nil {
				writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
				return
			}
		}
		client, err := s.store.provisionClient(input.Name, input.RedirectURIs, input.AuthMethod, scopes, false, true)
		if err != nil {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
			return
		}
		response := map[string]any{
			"client_id": client.ClientID, "client_id_issued_at": time.Now().Unix(),
			"client_name": input.Name, "redirect_uris": input.RedirectURIs,
			"token_endpoint_auth_method": input.AuthMethod, "scope": strings.Join(scopes, " "),
			"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		}
		if client.Secret != "" {
			response["client_secret"] = client.Secret
			response["client_secret_expires_at"] = 0
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response)
	})
}

// A global budget prevents many source addresses bypassing registration limits.
func (s *Service) checkRegistrationRate(w http.ResponseWriter) bool {
	s.rateMu.Lock()
	now := time.Now()
	if now.Sub(s.registrationWindow.start) >= time.Minute {
		s.registrationWindow = requestWindow{start: now}
	}
	s.registrationWindow.count++
	limited := s.registrationWindow.count > 5
	s.rateMu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "60")
		writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "registration rate limit exceeded")
	}
	return !limited
}
