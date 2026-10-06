package oauthserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-oauth2/oauth2/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	clientAuthNone    = "none"
	clientAuthBasic   = "client_secret_basic"
	clientAuthPost    = "client_secret_post"
	clientIDBytes     = 24
	clientSecretBytes = 32
)

type redirectURIContextKey struct{}

// ProvisionedClient is returned once by local client provisioning. The secret
// is empty for public clients and is never included in persisted state.
type ProvisionedClient struct {
	ClientID string
	Secret   string
}

// ProvisionClient creates a client registration. Confidential secrets are
// returned exactly once and only their bcrypt digest is persisted.
func (s *Store) ProvisionClient(name string, redirectURIs []string, authMethod string, scopes []string, allowAdmin bool) (ProvisionedClient, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return ProvisionedClient{}, errors.New("client name must be 1–120 characters")
	}
	if len(redirectURIs) == 0 || len(redirectURIs) > 20 {
		return ProvisionedClient{}, errors.New("client must have between 1 and 20 redirect URIs")
	}
	if authMethod != clientAuthNone && authMethod != clientAuthBasic && authMethod != clientAuthPost {
		return ProvisionedClient{}, errors.New("unsupported client authentication method")
	}
	for i, redirect := range redirectURIs {
		if err := validateRedirectURI(redirect); err != nil {
			return ProvisionedClient{}, fmt.Errorf("redirect URI %d: %w", i+1, err)
		}
		for prior := 0; prior < i; prior++ {
			if redirectURIs[prior] == redirect {
				return ProvisionedClient{}, errors.New("duplicate redirect URI")
			}
		}
	}
	scopes, err := NormaliseScopes(scopes)
	if err != nil || len(scopes) == 0 {
		return ProvisionedClient{}, errors.New("client must allow at least one valid action scope")
	}
	if !allowAdmin && slices.Contains(scopes, "settings") {
		return ProvisionedClient{}, errors.New("administrator scope is disabled")
	}

	id, err := randomOpaque("hmd_c_", clientIDBytes)
	if err != nil {
		return ProvisionedClient{}, err
	}
	created := ProvisionedClient{ClientID: id}
	secretDigest := ""
	if authMethod != clientAuthNone {
		created.Secret, err = randomOpaque("hmd_cs_", clientSecretBytes)
		if err != nil {
			return ProvisionedClient{}, err
		}
		digest, hashErr := bcrypt.GenerateFromPassword([]byte(created.Secret), bcrypt.DefaultCost)
		if hashErr != nil {
			return ProvisionedClient{}, fmt.Errorf("hashing client secret: %w", hashErr)
		}
		secretDigest = string(digest)
	}
	record := ClientRecord{
		ID: id, Name: name, RedirectURIs: append([]string(nil), redirectURIs...),
		AuthMethod: authMethod, SecretDigest: secretDigest,
		AllowedScopes: scopes, CreatedAt: time.Now().UTC(),
	}
	if err := s.Update(func(state *oauthState) error {
		if len(state.Clients) >= maxOAuthClients {
			return errors.New("OAuth client limit reached")
		}
		state.Clients[id] = record
		return nil
	}); err != nil {
		return ProvisionedClient{}, err
	}
	return created, nil
}

// DisableClient permanently disables a registration and revokes every grant
// and token issued to it. This is the local emergency path for a compromised
// client secret or a retired client.
func (s *Store) DisableClient(clientID string) error {
	return s.Update(func(state *oauthState) error {
		client, ok := state.Clients[clientID]
		if !ok {
			return errors.New("OAuth client not found")
		}
		client.Disabled = true
		state.Clients[clientID] = client

		now := time.Now().UTC()
		grantIDs := make(map[string]bool)
		for id, grant := range state.Grants {
			if grant.ClientID != clientID {
				continue
			}
			grant.RevokedAt = now
			grant.RevokedReason = "OAuth client disabled"
			state.Grants[id] = grant
			grantIDs[id] = true
		}
		familyIDs := make(map[string]bool)
		for id, family := range state.Families {
			if !grantIDs[family.GrantID] {
				continue
			}
			family.RevokedAt = now
			family.RevokedCause = "OAuth client disabled"
			state.Families[id] = family
			familyIDs[id] = true
		}
		for key, token := range state.Tokens {
			if !grantIDs[token.GrantID] && !familyIDs[token.FamilyID] {
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

func validateRedirectURI(raw string) error {
	if len(raw) == 0 || len(raw) > 2048 {
		return errors.New("must be between 1 and 2048 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("must be an absolute URI without user-info or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" || !isLoopbackRedirectHost(u.Hostname()) {
		return errors.New("must use HTTPS (HTTP is allowed only for loopback clients)")
	}
	return nil
}

func isLoopbackRedirectHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func randomOpaque(prefix string, bytes int) (string, error) {
	random := make([]byte, bytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generating OAuth credential: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(random), nil
}

type clientStore struct {
	store *Store
}

func (s clientStore) GetByID(ctx context.Context, id string) (oauth2.ClientInfo, error) {
	snapshot := transactionState(ctx)
	var state oauthState
	if snapshot != nil {
		state = *snapshot
	} else {
		var err error
		state, err = s.store.Snapshot()
		if err != nil {
			return nil, err
		}
	}
	record, ok := state.Clients[id]
	if !ok || record.Disabled {
		return nil, nil
	}
	redirect, _ := ctx.Value(redirectURIContextKey{}).(string)
	if !slices.Contains(record.RedirectURIs, redirect) {
		redirect = ""
	}
	if redirect == "" && len(record.RedirectURIs) > 0 {
		redirect = record.RedirectURIs[0]
	}
	base := oauthClient{record: record, redirect: redirect}
	if record.AuthMethod == clientAuthNone {
		return base, nil
	}
	return confidentialOAuthClient{oauthClient: base}, nil
}

type oauthClient struct {
	record   ClientRecord
	redirect string
}

func (c oauthClient) GetID() string     { return c.record.ID }
func (c oauthClient) GetSecret() string { return "" }
func (c oauthClient) GetDomain() string { return c.redirect }
func (c oauthClient) IsPublic() bool    { return c.record.AuthMethod == clientAuthNone }
func (c oauthClient) GetUserID() string { return "" }

type confidentialOAuthClient struct {
	oauthClient
}

func (c confidentialOAuthClient) VerifyPassword(secret string) bool {
	return c.record.SecretDigest != "" &&
		bcrypt.CompareHashAndPassword([]byte(c.record.SecretDigest), []byte(secret)) == nil
}
