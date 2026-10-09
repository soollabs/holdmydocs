package oauthserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	oauth2 "github.com/go-oauth2/oauth2/v4"
)

type pendingAuthorization struct {
	handleDigest  string
	flowDigest    string
	request       authorizationRequest
	createdAt     time.Time
	user          string
	sessionDigest string
	consuming     bool
}

type authorizationRequest struct {
	ClientID      string
	ClientName    string
	RedirectURI   string
	State         string
	Scopes        []string
	Resource      string
	Challenge     string
	ChallengeType string
}

type AuthorizationPrompt struct {
	Handle              string
	User                string
	ClientID            string
	ClientName          string
	RedirectHost        string
	Resource            string
	RequestedScopes     []string
	AvailableScopes     []string
	NamespaceNames      []string
	AdminScopeRequested bool
}

type AuthorizationStart struct {
	Handle        string
	FlowCookie    string
	LoginRequired bool
	Prompt        *AuthorizationPrompt
}

// StartAuthorization validates a new OAuth request or resumes one using only
// its opaque handle and browser-binding cookie.
func (s *Service) StartAuthorization(r *http.Request, session string, namespaceNames []string) (AuthorizationStart, error) {
	if len(r.URL.RawQuery) > 8192 {
		return AuthorizationStart{}, invalidRequest("authorisation query is too large")
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return AuthorizationStart{}, invalidRequest("malformed authorisation query")
	}
	if handles, exists := values["request"]; exists {
		if len(handles) != 1 || handles[0] == "" || len(values) != 1 {
			return AuthorizationStart{}, invalidRequest("invalid OAuth continuation")
		}
		handle := handles[0]
		flowCookie := requestCookie(r, "hmd_oauth_flow")
		if _, err := s.getPending(handle, flowCookie); err != nil {
			return AuthorizationStart{}, err
		}
		if session == "" {
			return AuthorizationStart{Handle: handle, LoginRequired: true}, nil
		}
		user, ok := s.auth.UserFor(session)
		if !ok || !s.auth.UserExists(user) {
			return AuthorizationStart{}, invalidRequest("browser session is no longer valid")
		}
		s.pendingMu.Lock()
		pending := s.pending[handle]
		if pending == nil || subtle.ConstantTimeCompare([]byte(pending.flowDigest), []byte(tokenDigest(flowCookie))) != 1 {
			s.pendingMu.Unlock()
			return AuthorizationStart{}, invalidRequest("OAuth continuation expired")
		}
		if pending.consuming {
			s.pendingMu.Unlock()
			return AuthorizationStart{}, invalidRequest("OAuth continuation is already being completed")
		}
		if pending.user == "" {
			pending.user = user
			pending.sessionDigest = tokenDigest(session)
		}
		bound := pending.user == user && digestMatches(pending.sessionDigest, session)
		pending = clonePending(pending)
		s.pendingMu.Unlock()
		if !bound {
			return AuthorizationStart{}, invalidRequest("OAuth continuation belongs to a different browser session")
		}
		return AuthorizationStart{Handle: handle, Prompt: s.prompt(handle, pending, user, namespaceNames)}, nil
	}

	request, err := s.parseAuthorizationRequest(values)
	if err != nil {
		return AuthorizationStart{}, err
	}
	handle, err := randomOpaque("hmd_oh_", 32)
	if err != nil {
		return AuthorizationStart{}, &ProtocolError{Code: "server_error", Description: "could not create authorisation request", Status: http.StatusInternalServerError}
	}
	flowCookie, err := randomOpaque("hmd_of_", 32)
	if err != nil {
		return AuthorizationStart{}, &ProtocolError{Code: "server_error", Description: "could not create browser binding", Status: http.StatusInternalServerError}
	}
	pending := &pendingAuthorization{
		handleDigest: tokenDigest(handle), flowDigest: tokenDigest(flowCookie),
		request: request, createdAt: time.Now().UTC(),
	}
	s.pendingMu.Lock()
	s.purgePendingLocked()
	if len(s.pending) >= maxPendingRequests {
		s.pendingMu.Unlock()
		return AuthorizationStart{}, &ProtocolError{Code: "temporarily_unavailable", Description: "too many pending authorisation requests", Status: http.StatusServiceUnavailable}
	}
	s.pending[handle] = pending
	s.pendingMu.Unlock()

	if session == "" {
		return AuthorizationStart{Handle: handle, FlowCookie: flowCookie, LoginRequired: true}, nil
	}
	user, ok := s.auth.UserFor(session)
	if !ok || !s.auth.UserExists(user) {
		return AuthorizationStart{}, invalidRequest("browser session is no longer valid")
	}
	s.pendingMu.Lock()
	pending.user = user
	pending.sessionDigest = tokenDigest(session)
	pending = clonePending(pending)
	s.pendingMu.Unlock()
	return AuthorizationStart{Handle: handle, FlowCookie: flowCookie, Prompt: s.prompt(handle, pending, user, namespaceNames)}, nil
}

func (s *Service) parseAuthorizationRequest(values url.Values) (authorizationRequest, error) {
	single := func(name string, required bool) (string, error) {
		items, exists := values[name]
		if !exists {
			if required {
				return "", invalidRequest("missing " + name)
			}
			return "", nil
		}
		if len(items) != 1 || required && items[0] == "" {
			return "", invalidRequest("invalid " + name)
		}
		return items[0], nil
	}
	// RFC 6749 section 3.1 requires unrecognised parameters to be ignored.
	// Read security-sensitive fields explicitly and reject their duplicates;
	// optional client hints must not alter the authorisation decision.
	clientID, err := single("client_id", true)
	if err != nil {
		return authorizationRequest{}, err
	}
	redirectURI, err := single("redirect_uri", true)
	if err != nil {
		return authorizationRequest{}, err
	}
	stateData, err := s.store.Snapshot()
	if err != nil {
		return authorizationRequest{}, &ProtocolError{Code: "server_error", Description: "could not read OAuth registrations", Status: http.StatusInternalServerError}
	}
	client, ok := stateData.Clients[clientID]
	if !ok || client.Disabled {
		return authorizationRequest{}, &ProtocolError{Code: "unauthorized_client", Description: "client is not registered", Status: http.StatusBadRequest}
	}
	// A redirect is never used, including on errors, until an exact registered
	// match has been established.
	if !slices.Contains(client.RedirectURIs, redirectURI) || validateRedirectURI(redirectURI) != nil {
		return authorizationRequest{}, &ProtocolError{Code: "invalid_request", Description: "redirect URI is not registered", Status: http.StatusBadRequest}
	}
	if !redirectQuerySafe(redirectURI) {
		return authorizationRequest{}, &ProtocolError{Code: "invalid_request", Description: "registered redirect URI uses a reserved response parameter", Status: http.StatusBadRequest}
	}
	// RFC 6749 section 4.1.2.1: the redirect URI and client are proven, so
	// every remaining authorisation error is returned to the client via a
	// redirect instead of an unauthenticated error page. Dynamically
	// registered clients are excluded: their callbacks are selected by the
	// registrant, so an error redirect would be an open-redirect/phishing
	// vector rather than a trusted allowlist match.
	redirectable := func(err *ProtocolError) *ProtocolError {
		if !client.Dynamic {
			err.RedirectURI, err.State = redirectURI, stateFrom(values)
		}
		return err
	}
	responseType, err := single("response_type", true)
	if err != nil || responseType != "code" {
		return authorizationRequest{}, redirectable(&ProtocolError{Code: "unsupported_response_type", Description: "only the authorisation code response is supported", Status: http.StatusBadRequest})
	}
	state, err := single("state", true)
	if err != nil || len(state) > 2048 {
		return authorizationRequest{}, redirectable(invalidRequest("state is required and must be at most 2048 characters"))
	}
	resource, err := single("resource", true)
	if err != nil {
		if protocol, ok := errors.AsType[*ProtocolError](err); ok {
			return authorizationRequest{}, redirectable(protocol)
		}
		return authorizationRequest{}, err
	}
	if ValidateResource([]string{resource}, s.options.Issuer+mcpResourcePath, true) != nil {
		return authorizationRequest{}, redirectable(&ProtocolError{Code: "invalid_target", Description: "unsupported resource", Status: http.StatusBadRequest})
	}
	challenge, err := single("code_challenge", true)
	if err != nil || ValidateS256Challenge(challenge) != nil {
		return authorizationRequest{}, redirectable(&ProtocolError{Code: "invalid_request", Description: "a valid S256 PKCE challenge is required", Status: http.StatusBadRequest})
	}
	method, err := single("code_challenge_method", true)
	if err != nil || method != "S256" {
		return authorizationRequest{}, redirectable(&ProtocolError{Code: "invalid_request", Description: "only S256 PKCE is supported", Status: http.StatusBadRequest})
	}
	responseScope, err := single("scope", false)
	if err != nil {
		if protocol, ok := errors.AsType[*ProtocolError](err); ok {
			return authorizationRequest{}, redirectable(protocol)
		}
		return authorizationRequest{}, err
	}
	if _, supplied := values["scope"]; supplied && responseScope == "" {
		return authorizationRequest{}, redirectable(&ProtocolError{Code: "invalid_scope", Description: "scope must be non-empty when supplied", Status: http.StatusBadRequest})
	}
	scopes := []string{"read"}
	if responseScope != "" {
		scopes, err = NormaliseScopes(strings.Fields(responseScope))
		if err != nil || len(scopes) == 0 {
			return authorizationRequest{}, redirectable(&ProtocolError{Code: "invalid_scope", Description: "unsupported action scope", Status: http.StatusBadRequest})
		}
		// Duplicate scope tokens are invalid rather than silently normalised.
		if len(scopes) != len(strings.Fields(responseScope)) {
			return authorizationRequest{}, redirectable(&ProtocolError{Code: "invalid_scope", Description: "duplicate action scope", Status: http.StatusBadRequest})
		}
	}
	if !scopeSubset(scopes, client.AllowedScopes) ||
		!s.options.AllowAdminDelegation && slices.Contains(scopes, "settings") {
		return authorizationRequest{}, redirectable(&ProtocolError{Code: "invalid_scope", Description: "requested action scope is not allowed for this client", Status: http.StatusBadRequest})
	}
	return authorizationRequest{
		ClientID: clientID, ClientName: client.Name, RedirectURI: redirectURI,
		State: state, Scopes: scopes, Resource: resource,
		Challenge: challenge, ChallengeType: "S256",
	}, nil
}

// stateFrom extracts the optional state echo for error redirects without
// requiring it elsewhere; the field is validated separately.
func stateFrom(values url.Values) string {
	if state, ok := values["state"]; ok && len(state) == 1 {
		return state[0]
	}
	return ""
}

// ErrorRedirect reports the RFC 6749 error redirect for an authorisation
// failure whose redirect URI was exactly validated, if one exists.
func ErrorRedirect(err error, issuer string) (string, bool) {
	protocol, ok := errors.AsType[*ProtocolError](err)
	if !ok || protocol.RedirectURI == "" || protocol.Code == "server_error" {
		return "", false
	}
	redirect, redirectErr := authorizationRedirect(
		authorizationRequest{RedirectURI: protocol.RedirectURI, State: protocol.State},
		map[string]string{"error": protocol.Code, "error_description": protocol.Description},
		issuer,
	)
	if redirectErr != nil {
		return "", false
	}
	return redirect, true
}

func redirectQuerySafe(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for _, key := range []string{"code", "state", "iss", "error", "error_description"} {
		if _, exists := q[key]; exists {
			return false
		}
	}
	return true
}

func (s *Service) prompt(handle string, pending *pendingAuthorization, user string, namespaceNames []string) *AuthorizationPrompt {
	userScopes := expandSettings(s.auth.Prefs(user).Scopes)
	available := make([]string, 0, len(pending.request.Scopes))
	state, err := s.store.Snapshot()
	if err == nil {
		client := state.Clients[pending.request.ClientID]
		clientScopes := expandSettings(client.AllowedScopes)
		for _, scope := range pending.request.Scopes {
			if slices.Contains(userScopes, scope) && slices.Contains(clientScopes, scope) &&
				(scope != "settings" || s.options.AllowAdminDelegation) {
				available = append(available, scope)
			}
		}
	}
	parsed, _ := url.Parse(pending.request.RedirectURI)
	host := ""
	if parsed != nil {
		host = parsed.Host
	}
	return &AuthorizationPrompt{
		Handle: handle, User: user, ClientID: pending.request.ClientID,
		ClientName: pending.request.ClientName, RedirectHost: host,
		Resource:            pending.request.Resource,
		RequestedScopes:     append([]string(nil), pending.request.Scopes...),
		AvailableScopes:     available,
		NamespaceNames:      append([]string(nil), namespaceNames...),
		AdminScopeRequested: slices.Contains(available, "settings"),
	}
}

// BindAuthorization attaches a pending browser request to a live HMD session.
func (s *Service) BindAuthorization(handle, flowCookie, user, session string) error {
	if flowCookie == "" || user == "" || session == "" {
		return invalidRequest("invalid OAuth browser binding")
	}
	currentUser, ok := s.auth.UserFor(session)
	if !ok || currentUser != user {
		return invalidRequest("browser session is not valid for this user")
	}
	if _, err := s.getPending(handle, flowCookie); err != nil {
		return err
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	pending := s.pending[handle]
	if pending == nil || subtle.ConstantTimeCompare([]byte(pending.flowDigest), []byte(tokenDigest(flowCookie))) != 1 {
		return invalidRequest("OAuth continuation expired")
	}
	if pending.consuming {
		return invalidRequest("OAuth continuation is already being completed")
	}
	if pending.user != "" && (pending.user != user || !digestMatches(pending.sessionDigest, session)) {
		return invalidRequest("OAuth continuation belongs to a different browser session")
	}
	pending.user = user
	pending.sessionDigest = tokenDigest(session)
	return nil
}

// CompleteAuthorization consumes the pending request, creates the consent
// grant and one-use code atomically, and returns the validated redirect.
func (s *Service) CompleteAuthorization(handle, flowCookie, user, session string, approve bool, scopes, namespaceMode string, namespaces, currentNamespaces []string) (string, error) {
	pending, err := s.claimPending(handle, flowCookie)
	if err != nil {
		return "", err
	}
	completed := false
	defer func() {
		if !completed {
			s.releasePending(handle)
		}
	}()
	currentUser, ok := s.auth.UserFor(session)
	if !ok || currentUser != user || pending.user != user || !digestMatches(pending.sessionDigest, session) {
		return "", invalidRequest("authorisation must use the same live browser session")
	}
	if !approve {
		s.consumePending(handle)
		completed = true
		return authorizationRedirect(pending.request, map[string]string{
			"error": "access_denied", "error_description": "The user denied the request",
		}, s.options.Issuer)
	}
	grantedScopes, err := NormaliseScopes(strings.Fields(scopes))
	if err != nil || len(grantedScopes) == 0 ||
		!scopeSubset(grantedScopes, pending.request.Scopes) {
		return "", &ProtocolError{Code: "invalid_scope", Description: "granted scopes must be a non-empty subset of the request", Status: http.StatusBadRequest}
	}
	state, err := s.store.Snapshot()
	if err != nil {
		return "", err
	}
	client, ok := state.Clients[pending.request.ClientID]
	if !ok || client.Disabled || !scopeSubset(grantedScopes, client.AllowedScopes) ||
		!s.options.AllowAdminDelegation && slices.Contains(grantedScopes, "settings") {
		return "", &ProtocolError{Code: "invalid_scope", Description: "client policy no longer permits these scopes", Status: http.StatusBadRequest}
	}
	if !scopeSubset(grantedScopes, s.auth.Prefs(user).Scopes) {
		return "", &ProtocolError{Code: "invalid_scope", Description: "your current HMD permissions do not allow these scopes", Status: http.StatusBadRequest}
	}
	if slices.Contains(grantedScopes, "settings") {
		// The settings scope is an explicit administrator grant and always
		// carries unrestricted namespace access, regardless of browser fields.
		namespaceMode = "all"
		namespaces = nil
	} else if namespaceMode == "all" {
		namespaces = nil
	} else if namespaceMode == "selected" {
		if len(namespaces) == 0 {
			return "", invalidRequest("select at least one namespace or explicitly allow all namespaces")
		}
		selected, err := validateNamespaceSelection(namespaces, currentNamespaces)
		if err != nil {
			return "", err
		}
		namespaces = selected
	} else {
		return "", &ProtocolError{Code: "invalid_request", Description: "choose selected namespaces or explicitly allow all namespaces", Status: http.StatusBadRequest}
	}

	grantID, err := randomOpaque("hmd_g_", 24)
	if err != nil {
		return "", err
	}
	familyID, err := randomOpaque("hmd_f_", 24)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	grant := GrantRecord{
		ID: grantID, User: user, ClientID: client.ID,
		Issuer: s.options.Issuer, Resource: pending.request.Resource,
		Scopes: grantedScopes, NamespaceMode: namespaceMode,
		Namespaces: append([]string{}, namespaces...),
		CreatedAt:  now, ExpiresAt: now.Add(grantFamilyTTL),
	}
	if namespaceMode == "all" {
		grant.Namespaces = nil
	}
	family := FamilyRecord{ID: familyID, GrantID: grantID, ExpiresAt: grant.ExpiresAt}

	var code string
	request := &http.Request{}
	ctx := context.WithValue(context.Background(), redirectURIContextKey{}, pending.request.RedirectURI)
	ctx = context.WithValue(ctx, trustedGrantIDKey, grantID)
	ctx = context.WithValue(ctx, trustedFamilyIDKey, familyID)
	ctx = context.WithValue(ctx, trustedIssuerKey, s.options.Issuer)
	ctx = context.WithValue(ctx, trustedResourceKey, pending.request.Resource)
	ctx = context.WithValue(ctx, trustedNamespaceModeKey, namespaceMode)
	ctx = context.WithValue(ctx, trustedNamespacesKey, grant.Namespaces)
	ctx = context.WithValue(ctx, trustedRefreshGenKey, "0")
	ctx = context.WithValue(ctx, trustedScopeKey, strings.Join(grantedScopes, " "))
	request = request.WithContext(ctx)
	tokenGenerate := &oauth2.TokenGenerateRequest{
		ClientID: client.ID, UserID: user, RedirectURI: pending.request.RedirectURI,
		Scope:               strings.Join(grantedScopes, " "),
		CodeChallenge:       pending.request.Challenge,
		CodeChallengeMethod: oauth2.CodeChallengeS256,
		Request:             request,
	}
	err = s.store.Transaction(ctx, func(tx context.Context) error {
		state := transactionState(tx)
		// Consent can wait for another writer. Recheck the registration and
		// live session here, not only against the earlier display/snapshot.
		current, exists := state.Clients[client.ID]
		currentUser, live := s.auth.UserFor(session)
		if !exists || current.Disabled || !slices.Contains(current.RedirectURIs, pending.request.RedirectURI) ||
			!scopeSubset(grantedScopes, current.AllowedScopes) ||
			!live || currentUser != user || !s.auth.UserExists(user) ||
			!scopeSubset(grantedScopes, s.auth.Prefs(user).Scopes) {
			return invalidRequest("permissions changed; start a new authorisation request")
		}
		state.Grants[grant.ID] = grant
		state.Families[family.ID] = family
		info, err := s.manager.GenerateAuthToken(tx, oauth2.Code, tokenGenerate)
		if err != nil {
			return err
		}
		code = info.GetCode()
		return nil
	})
	if err != nil {
		return "", err
	}
	redirect, err := authorizationRedirect(pending.request, map[string]string{"code": code}, s.options.Issuer)
	if err != nil {
		return "", err
	}
	s.consumePending(handle)
	completed = true
	return redirect, nil
}

func authorizationRedirect(request authorizationRequest, params map[string]string, issuer string) (string, error) {
	u, err := url.Parse(request.RedirectURI)
	if err != nil {
		return "", invalidRequest("registered redirect URI is invalid")
	}
	query := u.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	query.Set("state", request.State)
	query.Set("iss", issuer)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (s *Service) getPending(handle, flowCookie string) (*pendingAuthorization, error) {
	if handle == "" || flowCookie == "" {
		return nil, invalidRequest("OAuth browser binding is missing")
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.purgePendingLocked()
	pending := s.pending[handle]
	if pending == nil || pending.consuming ||
		subtle.ConstantTimeCompare([]byte(pending.handleDigest), []byte(tokenDigest(handle))) != 1 ||
		subtle.ConstantTimeCompare([]byte(pending.flowDigest), []byte(tokenDigest(flowCookie))) != 1 {
		return nil, invalidRequest("OAuth continuation expired")
	}
	return clonePending(pending), nil
}

func (s *Service) claimPending(handle, flowCookie string) (*pendingAuthorization, error) {
	if _, err := s.getPending(handle, flowCookie); err != nil {
		return nil, err
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	pending := s.pending[handle]
	if pending == nil || pending.consuming ||
		subtle.ConstantTimeCompare([]byte(pending.handleDigest), []byte(tokenDigest(handle))) != 1 ||
		subtle.ConstantTimeCompare([]byte(pending.flowDigest), []byte(tokenDigest(flowCookie))) != 1 {
		return nil, invalidRequest("OAuth continuation expired or already completed")
	}
	pending.consuming = true
	return clonePending(pending), nil
}

func (s *Service) releasePending(handle string) {
	s.pendingMu.Lock()
	if pending := s.pending[handle]; pending != nil {
		pending.consuming = false
	}
	s.pendingMu.Unlock()
}

func clonePending(pending *pendingAuthorization) *pendingAuthorization {
	copy := *pending
	copy.request.Scopes = append([]string(nil), pending.request.Scopes...)
	return &copy
}

func (s *Service) consumePending(handle string) {
	s.pendingMu.Lock()
	delete(s.pending, handle)
	s.pendingMu.Unlock()
}

func (s *Service) purgePendingLocked() {
	now := time.Now()
	for handle, pending := range s.pending {
		if now.Sub(pending.createdAt) > 10*time.Minute {
			delete(s.pending, handle)
		}
	}
}

func requestCookie(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func digestMatches(digest, raw string) bool {
	return digest != "" && subtle.ConstantTimeCompare([]byte(digest), []byte(tokenDigest(raw))) == 1
}

func invalidRequest(message string) *ProtocolError {
	return &ProtocolError{Code: "invalid_request", Description: message, Status: http.StatusBadRequest}
}
