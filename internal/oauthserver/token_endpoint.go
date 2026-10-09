package oauthserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	oauth2 "github.com/go-oauth2/oauth2/v4"
	oautherrors "github.com/go-oauth2/oauth2/v4/errors"
)

const maxOAuthFormBytes = 64 << 10

type parsedClientCredentials struct {
	id     string
	secret string
	record ClientRecord
}

func (s *Service) TokenHandler() http.Handler {
	return http.HandlerFunc(s.handleToken)
}

func (s *Service) RevocationHandler() http.Handler {
	return http.HandlerFunc(s.handleRevocation)
}

func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	setSensitiveHeaders(w)
	if !s.CheckRateLimit(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST is required")
		return
	}
	form, err := parseOAuthForm(w, r)
	if err != nil {
		writeProtocolError(w, err)
		return
	}
	credentials, err := s.authenticateClient(r, form)
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r, form, credentials)
	case "refresh_token":
		s.refreshTokens(w, r, form, credentials)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code and refresh_token grants are supported")
	}
}

func parseOAuthForm(w http.ResponseWriter, r *http.Request) (url.Values, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return nil, invalidRequest("token and revocation requests must be form encoded")
	}
	if r.URL.RawQuery != "" {
		return nil, invalidRequest("OAuth protocol parameters are not accepted in the query string")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxOAuthFormBytes))
	if err != nil {
		return nil, &ProtocolError{Code: "invalid_request", Description: "OAuth request body is too large or unreadable", Status: http.StatusBadRequest}
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, invalidRequest("malformed form body")
	}
	for _, values := range form {
		if len(values) != 1 {
			return nil, invalidRequest("duplicate form parameters are not accepted")
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.Form = form
	r.PostForm = form
	return form, nil
}

func (s *Service) authenticateClient(r *http.Request, form url.Values) (parsedClientCredentials, error) {
	if len(r.Header.Values("Authorization")) > 1 {
		return parsedClientCredentials{}, errors.New("multiple client authentication headers")
	}
	authHeader := r.Header.Get("Authorization")
	formID, formIDExists := singleFormValue(form, "client_id")
	formSecret, formSecretExists := singleFormValue(form, "client_secret")
	var id, secret string
	if authHeader != "" {
		scheme, encoded, basic := strings.Cut(strings.TrimSpace(authHeader), " ")
		if !basic || !strings.EqualFold(scheme, "Basic") || encoded == "" ||
			formIDExists || formSecretExists {
			return parsedClientCredentials{}, errors.New("mixed or unsupported client authentication")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return parsedClientCredentials{}, errors.New("malformed basic authentication")
		}
		username, password, ok := strings.Cut(string(decoded), ":")
		if !ok {
			return parsedClientCredentials{}, errors.New("malformed basic credentials")
		}
		id, err = url.QueryUnescape(username)
		if err != nil {
			return parsedClientCredentials{}, errors.New("malformed basic client id")
		}
		secret, err = url.QueryUnescape(password)
		if err != nil {
			return parsedClientCredentials{}, errors.New("malformed basic client secret")
		}
	} else {
		id = formID
		secret = formSecret
	}
	if id == "" {
		return parsedClientCredentials{}, errors.New("client id is missing")
	}
	state, err := s.store.Snapshot()
	if err != nil {
		return parsedClientCredentials{}, err
	}
	client, ok := state.Clients[id]
	if !ok || client.Disabled {
		return parsedClientCredentials{}, errors.New("client is unknown or disabled")
	}
	switch client.AuthMethod {
	case clientAuthNone:
		if authHeader != "" || formSecretExists || secret != "" {
			return parsedClientCredentials{}, errors.New("public client supplied a secret")
		}
	case clientAuthBasic:
		if authHeader == "" || formIDExists || formSecretExists || !verifyClientSecret(client, secret) {
			return parsedClientCredentials{}, errors.New("confidential client authentication failed")
		}
	case clientAuthPost:
		if authHeader != "" || !formSecretExists || !verifyClientSecret(client, secret) {
			return parsedClientCredentials{}, errors.New("confidential client authentication failed")
		}
	default:
		return parsedClientCredentials{}, errors.New("unsupported client authentication method")
	}
	return parsedClientCredentials{id: id, secret: secret, record: client}, nil
}

func verifyClientSecret(client ClientRecord, secret string) bool {
	if client.SecretDigest == "" || secret == "" {
		return false
	}
	return (confidentialOAuthClient{record: client}).VerifyPassword(secret)
}

func (s *Service) exchangeCode(w http.ResponseWriter, r *http.Request, form url.Values, client parsedClientCredentials) {
	code, codeOK := singleFormValue(form, "code")
	redirectURI, redirectOK := singleFormValue(form, "redirect_uri")
	verifier, verifierOK := singleFormValue(form, "code_verifier")
	resources := form["resource"]
	if !codeOK || !redirectOK || !verifierOK || code == "" || redirectURI == "" || verifier == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code, redirect_uri and code_verifier are required")
		return
	}
	resource := ""
	if len(resources) == 1 {
		resource = resources[0]
	}
	if len(resources) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "resource is required")
		return
	}
	if err := ValidateResource(resources, s.options.Issuer+mcpResourcePath, true); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "unsupported resource")
		return
	}
	snapshot, err := s.store.Snapshot()
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "OAuth state is unavailable")
		return
	}
	codeRecord, ok := snapshot.Tokens["c:"+tokenDigest(code)]
	if !ok || codeRecord.CodeDigest != tokenDigest(code) ||
		!codeRecord.RevokedAt.IsZero() ||
		codeRecord.ConsumedAt.IsZero() && !codeRecord.CodeExpiresAt.IsZero() && time.Now().After(codeRecord.CodeExpiresAt) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorisation code is invalid or expired")
		return
	}
	if codeRecord.ClientID != client.id || codeRecord.RedirectURI != redirectURI ||
		codeRecord.Resource != resource || codeRecord.Issuer != s.options.Issuer {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorisation code binding does not match")
		return
	}
	if ValidatePKCEVerifier(verifier, codeRecord.PKCEChallenge) != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	if !codeRecord.ConsumedAt.IsZero() {
		// RFC 6749 section 4.1.2: revoke credentials derived from a reused
		// code. Verify client, callback, resource and PKCE first so knowledge
		// of a code alone cannot be used to disconnect its owner.
		if err := s.revokeFamily(codeRecord.FamilyID, "authorisation code reuse"); err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not revoke reused code family")
			return
		}
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorisation code reuse revoked the token family")
		return
	}
	grant, ok := snapshot.Grants[codeRecord.GrantID]
	if !ok || !grant.RevokedAt.IsZero() || time.Now().After(grant.ExpiresAt) ||
		grant.Resource != resource || grant.Issuer != s.options.Issuer {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorisation grant is no longer active")
		return
	}
	effective := EffectiveScopes(codeRecord.Scopes, grant.Scopes, s.auth.Prefs(grant.User).Scopes, client.record.AllowedScopes)
	if !s.auth.UserExists(grant.User) || !scopesSemanticallyEqual(effective, codeRecord.Scopes) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "current HMD permissions require new consent")
		return
	}
	request := requestWithTrustedResource(r, s.options.Issuer, resource, redirectURI)
	generate := &oauth2.TokenGenerateRequest{
		ClientID: client.id, ClientSecret: client.secret, RedirectURI: redirectURI,
		Code: code, CodeVerifier: verifier, Request: request,
	}
	var info oauth2.TokenInfo
	replayDetected := false
	transactionContext := context.WithValue(r.Context(), redirectURIContextKey{}, redirectURI)
	err = s.store.Transaction(transactionContext, func(ctx context.Context) error {
		state := transactionState(ctx)
		current := state.Tokens["c:"+tokenDigest(code)]
		if current.CodeDigest != tokenDigest(code) || !current.RevokedAt.IsZero() ||
			current.ClientID != client.id || current.Resource != resource ||
			current.RedirectURI != redirectURI || current.Issuer != s.options.Issuer ||
			ValidatePKCEVerifier(verifier, current.PKCEChallenge) != nil {
			return oautherrors.ErrInvalidAuthorizeCode
		}
		if !current.ConsumedAt.IsZero() {
			if err := revokeFamilyState(state, current.FamilyID, "authorisation code reuse", time.Now().UTC()); err != nil {
				return err
			}
			replayDetected = true
			return nil
		}
		currentGrant, exists := state.Grants[current.GrantID]
		if !exists || !currentGrant.RevokedAt.IsZero() ||
			!currentGrant.ExpiresAt.IsZero() && time.Now().After(currentGrant.ExpiresAt) {
			return oautherrors.ErrInvalidAuthorizeCode
		}
		currentScopes, err := s.currentTokenScopes(state, current, client.id)
		if err != nil || !scopesSemanticallyEqual(currentScopes, current.Scopes) {
			return oautherrors.ErrInvalidAuthorizeCode
		}
		info, err = s.manager.GenerateAccessToken(ctx, oauth2.AuthorizationCode, generate)
		if err != nil {
			return err
		}
		bindTokenFamilyState(state, info, 0, currentGrant.ExpiresAt)
		return nil
	})
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorisation code could not be redeemed")
		return
	}
	if replayDetected {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorisation code reuse revoked the token family")
		return
	}
	writeTokenResponse(w, info)
}

func (s *Service) refreshTokens(w http.ResponseWriter, r *http.Request, form url.Values, client parsedClientCredentials) {
	refresh, ok := singleFormValue(form, "refresh_token")
	if !ok || refresh == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	resources, supplied := form["resource"]
	if supplied && ValidateResource(resources, s.options.Issuer+mcpResourcePath, true) != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "unsupported resource")
		return
	}
	snapshot, err := s.store.Snapshot()
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "OAuth state is unavailable")
		return
	}
	refreshKey := "r:" + tokenDigest(refresh)
	record, exists := snapshot.Tokens[refreshKey]
	if !exists || record.RefreshDigest != tokenDigest(refresh) || record.ClientID != client.id {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid")
		return
	}
	grant, grantOK := snapshot.Grants[record.GrantID]
	family, familyOK := snapshot.Families[record.FamilyID]
	if !grantOK || !familyOK || !grant.RevokedAt.IsZero() || !family.RevokedAt.IsZero() ||
		!grant.ExpiresAt.IsZero() && time.Now().After(grant.ExpiresAt) ||
		!family.ExpiresAt.IsZero() && time.Now().After(family.ExpiresAt) ||
		record.Issuer != s.options.Issuer || record.Resource != s.options.Issuer+mcpResourcePath {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh grant is no longer active")
		return
	}
	if !record.RefreshUsedAt.IsZero() {
		if err := s.revokeFamily(record.FamilyID, "refresh token reuse"); err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not revoke reused refresh token family")
			return
		}
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse revoked the token family")
		return
	}
	if !record.RefreshExpiresAt.IsZero() && time.Now().After(record.RefreshExpiresAt) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token has expired")
		return
	}
	if resource, supplied := singleFormValue(form, "resource"); supplied && resource != record.Resource {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource does not match the refresh grant")
		return
	}
	requestedScope, hasScope := singleFormValue(form, "scope")
	requested := record.Scopes
	if hasScope {
		requested, err = NormaliseScopes(strings.Fields(requestedScope))
		if err != nil || len(requested) == 0 || !scopeSubset(requested, record.Scopes) || !scopeSubset(requested, grant.Scopes) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_scope", "refresh scope must be a non-empty subset of the grant")
			return
		}
	}
	if !s.auth.UserExists(record.User) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "HMD user no longer exists")
		return
	}
	effective := EffectiveScopes(requested, grant.Scopes, s.auth.Prefs(record.User).Scopes, client.record.AllowedScopes)
	if len(effective) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", "current HMD permissions do not allow this refresh")
		return
	}
	resource := record.Resource
	request := requestWithTrustedResource(r, record.Issuer, resource, "")
	generate := &oauth2.TokenGenerateRequest{
		ClientID: client.id, ClientSecret: client.secret, Refresh: refresh,
		Scope: strings.Join(effective, " "), Request: request,
	}
	var info oauth2.TokenInfo
	replayDetected := false
	err = s.store.Transaction(r.Context(), func(ctx context.Context) error {
		state := transactionState(ctx)
		currentKey := "r:" + tokenDigest(refresh)
		current := state.Tokens[currentKey]
		if current.ClientID != client.id || current.RefreshDigest != tokenDigest(refresh) ||
			current.FamilyID != record.FamilyID {
			return oautherrors.ErrInvalidRefreshToken
		}
		if _, _, err := s.activeTokenBinding(state, current, client.id); err != nil {
			return oautherrors.ErrInvalidRefreshToken
		}
		if !current.RefreshUsedAt.IsZero() {
			if err := revokeFamilyState(state, current.FamilyID, "refresh token reuse", time.Now().UTC()); err != nil {
				return err
			}
			replayDetected = true
			return nil
		}
		currentScopes, err := s.currentTokenScopes(state, current, client.id)
		if err != nil {
			return oautherrors.ErrInvalidRefreshToken
		}
		if !current.RefreshExpiresAt.IsZero() && time.Now().After(current.RefreshExpiresAt) {
			return oautherrors.ErrInvalidRefreshToken
		}
		// Recompute from the transaction's authoritative token and policies,
		// rather than the earlier snapshot. Removed scopes cannot reappear
		// after a later user permission increase.
		currentRequested := current.Scopes
		if hasScope {
			currentRequested = requested
			if !scopeSubset(currentRequested, current.Scopes) {
				return oautherrors.ErrInvalidRefreshToken
			}
		}
		generate.Scope = strings.Join(EffectiveScopes(currentRequested, currentScopes, currentScopes, currentScopes), " ")
		if generate.Scope == "" {
			return oautherrors.ErrInvalidRefreshToken
		}
		family := state.Families[current.FamilyID]
		family.Generation++
		state.Families[family.ID] = family
		current.RefreshGeneration = family.Generation
		state.Tokens[currentKey] = current
		info, err = s.manager.RefreshAccessToken(ctx, generate)
		if err != nil {
			return err
		}
		current = state.Tokens[currentKey]
		current.RefreshUsedAt = time.Now().UTC()
		state.Tokens[currentKey] = current
		if current.AccessDigest != "" {
			delete(state.Tokens, "a:"+current.AccessDigest)
		}
		bindTokenFamilyState(state, info, family.Generation, family.ExpiresAt)
		return nil
	})
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token could not be rotated")
		return
	}
	if replayDetected {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse revoked the token family")
		return
	}
	writeTokenResponse(w, info)
}

// currentTokenScopes must run inside the store transaction for issuance and
// rotation. Snapshot checks alone can race disconnect, replay and policy
// changes while a request is waiting for the transaction lock.
func (s *Service) currentTokenScopes(state *oauthState, token TokenRecord, clientID string) ([]string, error) {
	grant, client, err := s.activeTokenBinding(state, token, clientID)
	if err != nil || !s.auth.UserExists(token.User) {
		return nil, oautherrors.ErrInvalidRefreshToken
	}
	clientScopes := client.AllowedScopes
	if !s.options.AllowAdminDelegation {
		clientScopes = withoutSettings(clientScopes)
	}
	effective := EffectiveScopes(token.Scopes, grant.Scopes, s.auth.Prefs(token.User).Scopes, clientScopes)
	if len(effective) == 0 {
		return nil, oautherrors.ErrInvalidRefreshToken
	}
	return effective, nil
}

func (s *Service) activeTokenBinding(state *oauthState, token TokenRecord, clientID string) (GrantRecord, ClientRecord, error) {
	now := time.Now()
	grant, grantOK := state.Grants[token.GrantID]
	family, familyOK := state.Families[token.FamilyID]
	client, clientOK := state.Clients[clientID]
	if !grantOK || !familyOK || !clientOK || client.Disabled ||
		token.ClientID != clientID || grant.ClientID != clientID ||
		grant.User != token.User || family.GrantID != grant.ID ||
		token.Issuer != s.options.Issuer || grant.Issuer != s.options.Issuer ||
		token.Resource != s.options.Issuer+mcpResourcePath || grant.Resource != token.Resource ||
		!token.RevokedAt.IsZero() || !grant.RevokedAt.IsZero() || !family.RevokedAt.IsZero() ||
		!grant.ExpiresAt.IsZero() && now.After(grant.ExpiresAt) ||
		!family.ExpiresAt.IsZero() && now.After(family.ExpiresAt) {
		return GrantRecord{}, ClientRecord{}, oautherrors.ErrInvalidRefreshToken
	}
	return grant, client, nil
}

func (s *Service) revokeFamily(familyID, reason string) error {
	return s.store.Update(func(state *oauthState) error {
		return revokeFamilyState(state, familyID, reason, time.Now().UTC())
	})
}

func revokeFamilyState(state *oauthState, familyID, reason string, now time.Time) error {
	family, ok := state.Families[familyID]
	if !ok {
		return nil
	}
	family.RevokedAt = now
	family.RevokedCause = reason
	state.Families[familyID] = family
	for key, token := range state.Tokens {
		if token.FamilyID != familyID {
			continue
		}
		if strings.HasPrefix(key, "a:") {
			delete(state.Tokens, key)
		} else {
			token.RevokedAt = now
			state.Tokens[key] = token
		}
	}
	return nil
}

func (s *Service) handleRevocation(w http.ResponseWriter, r *http.Request) {
	setSensitiveHeaders(w)
	if !s.CheckRateLimit(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST is required")
		return
	}
	form, err := parseOAuthForm(w, r)
	if err != nil {
		writeProtocolError(w, err)
		return
	}
	client, err := s.authenticateClient(r, form)
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	token, ok := singleFormValue(form, "token")
	if !ok || token == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	// RFC 7009 section 2.1: an unknown hint is ignored. Both supported token
	// indexes are searched below, regardless of the caller's hint.
	state, err := s.store.Snapshot()
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "OAuth state is unavailable")
		return
	}
	record, found := state.Tokens["a:"+tokenDigest(token)]
	if !found || record.AccessDigest != tokenDigest(token) {
		record, found = state.Tokens["r:"+tokenDigest(token)]
		if !found || record.RefreshDigest != tokenDigest(token) {
			// RFC 7009 intentionally does not disclose unknown token state.
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	if record.ClientID != client.id {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := s.revokeGrant(record.GrantID, "client revocation"); err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not revoke OAuth grant")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Service) revokeGrant(grantID, reason string) error {
	return s.store.Update(func(state *oauthState) error {
		grant, ok := state.Grants[grantID]
		if !ok {
			return nil
		}
		now := time.Now().UTC()
		grant.RevokedAt = now
		grant.RevokedReason = reason
		state.Grants[grantID] = grant
		families := make(map[string]bool)
		for id, family := range state.Families {
			if family.GrantID != grantID {
				continue
			}
			family.RevokedAt = now
			family.RevokedCause = reason
			state.Families[id] = family
			families[id] = true
		}
		for key, token := range state.Tokens {
			if token.GrantID != grantID && !families[token.FamilyID] {
				continue
			}
			if strings.HasPrefix(key, "a:") {
				delete(state.Tokens, key)
			} else {
				token.RevokedAt = now
				state.Tokens[key] = token
			}
		}
		return nil
	})
}

func requestWithTrustedResource(r *http.Request, issuer, resource, redirect string) *http.Request {
	ctx := context.WithValue(r.Context(), trustedIssuerKey, issuer)
	ctx = context.WithValue(ctx, trustedResourceKey, resource)
	ctx = context.WithValue(ctx, redirectURIContextKey{}, redirect)
	return r.WithContext(ctx)
}

func bindTokenFamilyState(state *oauthState, info oauth2.TokenInfo, generation uint64, absoluteExpiry time.Time) {
	if info == nil {
		return
	}
	if extendable, ok := info.(oauth2.ExtendableTokenInfo); ok {
		values := extendable.GetExtension()
		if values == nil {
			values = make(url.Values)
		}
		values.Set("hmd_refresh_generation", strconv.FormatUint(generation, 10))
		extendable.SetExtension(values)
	}
	record := tokenRecordFromInfo(info)
	for _, key := range []string{"a:" + record.AccessDigest, "r:" + record.RefreshDigest} {
		if strings.HasSuffix(key, ":") {
			continue
		}
		if existing, ok := state.Tokens[key]; ok {
			existing.RefreshGeneration = generation
			existing.RefreshExpiresAt = absoluteExpiry
			state.Tokens[key] = existing
		}
	}
}

func scopesSemanticallyEqual(left, right []string) bool {
	return scopeSubset(left, right) && scopeSubset(right, left)
}

func singleFormValue(form url.Values, key string) (string, bool) {
	values, ok := form[key]
	return func() string {
		if !ok || len(values) != 1 {
			return ""
		}
		return values[0]
	}(), ok && len(values) == 1
}

func setSensitiveHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeTokenResponse(w http.ResponseWriter, info oauth2.TokenInfo) {
	if info == nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token issuance failed")
		return
	}
	response := map[string]any{
		"access_token": info.GetAccess(),
		"token_type":   "Bearer",
		"expires_in":   int64(info.GetAccessExpiresIn() / time.Second),
	}
	if scope := strings.TrimSpace(info.GetScope()); scope != "" {
		response["scope"] = scope
	}
	if refresh := info.GetRefresh(); refresh != "" {
		response["refresh_token"] = refresh
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func writeProtocolError(w http.ResponseWriter, err error) {
	if protocol, ok := errors.AsType[*ProtocolError](err); ok {
		writeOAuthError(w, protocol.Status, protocol.Code, protocol.Description)
		return
	}
	writeOAuthError(w, http.StatusBadRequest, "invalid_request", "OAuth request is invalid")
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	setSensitiveHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}
