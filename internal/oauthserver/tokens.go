package oauthserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-oauth2/oauth2/v4"
	"github.com/go-oauth2/oauth2/v4/models"
)

var errOAuthTransactionRequired = errors.New("OAuth state mutation requires a store transaction")

type tokenStore struct {
	store *Store
}

func tokenKey(kind, raw string) string {
	return kind + ":" + tokenDigest(raw)
}

func tokenDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s tokenStore) state(ctx context.Context) (oauthState, *oauthState, error) {
	if tx := transactionState(ctx); tx != nil {
		return *tx, tx, nil
	}
	state, err := s.store.Snapshot()
	return state, nil, err
}

func (s tokenStore) Create(ctx context.Context, info oauth2.TokenInfo) error {
	state := transactionState(ctx)
	if state == nil {
		return errOAuthTransactionRequired
	}
	if info == nil {
		return errors.New("OAuth token information is nil")
	}
	record := tokenRecordFromInfo(info)
	if record.GrantID == "" {
		return errors.New("OAuth token is missing its trusted grant binding")
	}
	if record.CodeDigest == "" && record.AccessDigest == "" && record.RefreshDigest == "" {
		return errors.New("OAuth token contains no credentials")
	}
	entries := 0
	for _, value := range []string{record.CodeDigest, record.AccessDigest, record.RefreshDigest} {
		if value != "" {
			entries++
		}
	}
	if len(state.Tokens)+entries > maxOAuthTokens {
		return errors.New("OAuth token limit reached")
	}
	if record.CodeDigest != "" {
		state.Tokens["c:"+record.CodeDigest] = record
	}
	if record.AccessDigest != "" {
		state.Tokens["a:"+record.AccessDigest] = record
	}
	if record.RefreshDigest != "" {
		state.Tokens["r:"+record.RefreshDigest] = record
	}
	return nil
}

func tokenRecordFromInfo(info oauth2.TokenInfo) TokenRecord {
	now := info.GetAccessCreateAt()
	if now.IsZero() {
		now = info.GetCodeCreateAt()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	record := TokenRecord{
		ClientID: info.GetClientID(), User: info.GetUserID(), RedirectURI: info.GetRedirectURI(),
		Scopes: strings.Fields(info.GetScope()), CreatedAt: now.UTC(),
		CodeExpiresAt:    expiration(info.GetCodeCreateAt(), info.GetCodeExpiresIn()),
		AccessExpiresAt:  expiration(info.GetAccessCreateAt(), info.GetAccessExpiresIn()),
		RefreshExpiresAt: expiration(info.GetRefreshCreateAt(), info.GetRefreshExpiresIn()),
		PKCEChallenge:    info.GetCodeChallenge(),
		PKCEChallengeAlg: string(info.GetCodeChallengeMethod()),
	}
	if info.GetCode() != "" {
		record.CodeDigest = tokenDigest(info.GetCode())
	}
	if info.GetAccess() != "" {
		record.AccessDigest = tokenDigest(info.GetAccess())
	}
	if info.GetRefresh() != "" {
		record.RefreshDigest = tokenDigest(info.GetRefresh())
	}
	if extendable, ok := info.(oauth2.ExtendableTokenInfo); ok {
		extension := extendable.GetExtension()
		record.GrantID = extension.Get("hmd_grant_id")
		record.FamilyID = extension.Get("hmd_family_id")
		record.Issuer = extension.Get("hmd_issuer")
		record.Resource = extension.Get("hmd_resource")
		record.NamespaceMode = extension.Get("hmd_namespace_mode")
		record.Namespaces = extension["hmd_namespaces"]
		record.RefreshGeneration, _ = strconv.ParseUint(extension.Get("hmd_refresh_generation"), 10, 64)
	}
	return record
}

func expiration(created time.Time, duration time.Duration) time.Time {
	if created.IsZero() || duration <= 0 {
		return time.Time{}
	}
	return created.Add(duration).UTC()
}

func (s tokenStore) GetByCode(ctx context.Context, code string) (oauth2.TokenInfo, error) {
	state, _, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	record, ok := state.Tokens["c:"+tokenDigest(code)]
	if !ok || !record.ConsumedAt.IsZero() || !record.RevokedAt.IsZero() || record.CodeDigest != tokenDigest(code) ||
		!record.CodeExpiresAt.IsZero() && time.Now().After(record.CodeExpiresAt) {
		return nil, nil
	}
	return tokenInfo(record, code, "", ""), nil
}

func (s tokenStore) GetByAccess(ctx context.Context, access string) (oauth2.TokenInfo, error) {
	state, _, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	record, ok := state.Tokens["a:"+tokenDigest(access)]
	if !ok || !record.RevokedAt.IsZero() || record.AccessDigest != tokenDigest(access) {
		return nil, nil
	}
	return tokenInfo(record, "", access, ""), nil
}

func (s tokenStore) GetByRefresh(ctx context.Context, refresh string) (oauth2.TokenInfo, error) {
	state, _, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	record, ok := state.Tokens["r:"+tokenDigest(refresh)]
	if !ok || record.RefreshDigest != tokenDigest(refresh) ||
		!record.RevokedAt.IsZero() ||
		!record.RefreshExpiresAt.IsZero() && time.Now().After(record.RefreshExpiresAt) {
		return nil, nil
	}
	return tokenInfo(record, "", "", refresh), nil
}

func tokenInfo(record TokenRecord, code, access, refresh string) *models.Token {
	token := models.NewToken()
	token.SetClientID(record.ClientID)
	token.SetUserID(record.User)
	token.SetRedirectURI(record.RedirectURI)
	token.SetScope(strings.Join(record.Scopes, " "))
	token.SetCode(code)
	token.SetCodeChallenge(record.PKCEChallenge)
	token.SetCodeChallengeMethod(oauth2.CodeChallengeMethod(record.PKCEChallengeAlg))
	token.SetCodeCreateAt(record.CreatedAt)
	if !record.CodeExpiresAt.IsZero() {
		token.SetCodeExpiresIn(record.CodeExpiresAt.Sub(record.CreatedAt))
	}
	token.SetAccess(access)
	token.SetAccessCreateAt(record.CreatedAt)
	if !record.AccessExpiresAt.IsZero() {
		token.SetAccessExpiresIn(record.AccessExpiresAt.Sub(record.CreatedAt))
	}
	token.SetRefresh(refresh)
	token.SetRefreshCreateAt(record.CreatedAt)
	if !record.RefreshExpiresAt.IsZero() {
		token.SetRefreshExpiresIn(record.RefreshExpiresAt.Sub(record.CreatedAt))
	}
	token.SetExtension(url.Values{
		"hmd_grant_id":           {record.GrantID},
		"hmd_family_id":          {record.FamilyID},
		"hmd_issuer":             {record.Issuer},
		"hmd_resource":           {record.Resource},
		"hmd_namespace_mode":     {record.NamespaceMode},
		"hmd_namespaces":         append([]string(nil), record.Namespaces...),
		"hmd_refresh_generation": {strconv.FormatUint(record.RefreshGeneration, 10)},
	})
	return token
}

func (s tokenStore) RemoveByCode(ctx context.Context, code string) error {
	state := transactionState(ctx)
	if state == nil {
		return errOAuthTransactionRequired
	}
	key := "c:" + tokenDigest(code)
	record, ok := state.Tokens[key]
	if ok && record.CodeDigest == tokenDigest(code) {
		record.ConsumedAt = time.Now().UTC()
		state.Tokens[key] = record
	}
	return nil
}

func (s tokenStore) RemoveByAccess(ctx context.Context, access string) error {
	state := transactionState(ctx)
	if state == nil {
		return errOAuthTransactionRequired
	}
	key := "a:" + tokenDigest(access)
	if record, ok := state.Tokens[key]; ok && record.AccessDigest == tokenDigest(access) {
		delete(state.Tokens, key)
	}
	return nil
}

func (s tokenStore) RemoveByRefresh(ctx context.Context, refresh string) error {
	state := transactionState(ctx)
	if state == nil {
		return errOAuthTransactionRequired
	}
	key := "r:" + tokenDigest(refresh)
	if record, ok := state.Tokens[key]; ok && record.RefreshDigest == tokenDigest(refresh) {
		record.RefreshUsedAt = time.Now().UTC()
		state.Tokens[key] = record
	}
	return nil
}

type accessGenerator struct{}

func (accessGenerator) Token(_ context.Context, _ *oauth2.GenerateBasic, withRefresh bool) (string, string, error) {
	access, err := randomOpaque("hmd_oa_", 32)
	if err != nil {
		return "", "", err
	}
	var refresh string
	if withRefresh {
		refresh, err = randomOpaque("hmd_or_", 32)
		if err != nil {
			return "", "", err
		}
	}
	return access, refresh, nil
}

type authorizeGenerator struct{}

func (authorizeGenerator) Token(_ context.Context, _ *oauth2.GenerateBasic) (string, error) {
	code, err := randomOpaque("hmd_oc_", 32)
	if err != nil {
		return "", err
	}
	return code, nil
}

func requireOAuthTransaction(ctx context.Context) (*oauthState, error) {
	state := transactionState(ctx)
	if state == nil {
		return nil, fmt.Errorf("%w", errOAuthTransactionRequired)
	}
	return state, nil
}
