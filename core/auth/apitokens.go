package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// APITokenPrefix marks a raw personal access token so Authenticate can route it
// without a speculative session lookup. Session secrets are uppercase base32
// (ids.Secret) and can never begin with this lowercase, underscore-bearing
// string, so the token and session spaces are disjoint.
const APITokenPrefix = "cyp_"

// ErrTokenNotFound is returned when a token id does not exist or is not owned by
// the caller (the two are indistinguishable to the caller by design).
var ErrTokenNotFound = errors.New("auth: token not found")

// ErrInvalidAbility is returned when a token is requested with an unknown or
// empty ability set.
var ErrInvalidAbility = errors.New("auth: abilities must be a non-empty subset of read, write, deploy, env, servers, admin")

// CreateToken issues a personal access token for userID. The raw token is
// returned exactly once (never stored — only its hash is); name is a
// human-readable label; expiresAt nil means the token never expires. The token
// authenticates as its owning user, inheriting that user's role and team
// memberships, narrowed further by abilities — a token can never do something
// its owner could not. An empty ability set is rejected rather than silently
// widened: a credential's authority is always an explicit choice.
//
// projectID, when set, narrows the token to one project: abilities say what it
// may do, this says where. The caller is responsible for having checked that
// the project exists and that the owner can reach it — this layer only stores
// the decision.
func (a *Authenticator) CreateToken(ctx context.Context, userID, name string, abilities []domain.Ability, expiresAt *time.Time, projectID string) (raw string, tok domain.APIToken, err error) {
	if len(abilities) == 0 {
		return "", domain.APIToken{}, ErrInvalidAbility
	}
	seen := map[domain.Ability]bool{}
	deduped := make([]domain.Ability, 0, len(abilities))
	for _, ab := range abilities {
		if !domain.ValidAbility(ab) {
			return "", domain.APIToken{}, ErrInvalidAbility
		}
		if !seen[ab] {
			seen[ab] = true
			deduped = append(deduped, ab)
		}
	}
	raw = APITokenPrefix + ids.Secret()
	tok, err = a.store.CreateAPIToken(ctx, ids.New(ids.PrefixAPIToken), userID, name, deduped, HashToken(raw), expiresAt, projectID)
	if err != nil {
		return "", domain.APIToken{}, fmt.Errorf("auth: creating api token: %w", err)
	}
	return raw, tok, nil
}

// ListTokens returns a user's tokens (metadata only, never the secret).
func (a *Authenticator) ListTokens(ctx context.Context, userID string) ([]domain.APIToken, error) {
	return a.store.ListAPITokensByUser(ctx, userID)
}

// DeleteToken revokes tokenID, but only if it belongs to userID. A token that
// does not exist or is owned by someone else yields ErrTokenNotFound — a caller
// can neither revoke nor probe for others' tokens.
func (a *Authenticator) DeleteToken(ctx context.Context, userID, tokenID string) error {
	tok, err := a.store.GetAPIToken(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrTokenNotFound
		}
		return fmt.Errorf("auth: loading api token: %w", err)
	}
	if tok.UserID != userID {
		return ErrTokenNotFound
	}
	if err := a.store.DeleteAPIToken(ctx, tokenID); err != nil {
		return fmt.Errorf("auth: deleting api token: %w", err)
	}
	return nil
}

// authenticateAPIToken resolves a raw API token to its principal — owning user
// plus the abilities the token was issued with — and records the use. The
// last-used update is best-effort; it must never fail a request.
func (a *Authenticator) authenticateAPIToken(ctx context.Context, rawToken string) (Principal, error) {
	if strings.TrimPrefix(rawToken, APITokenPrefix) == "" {
		return Principal{}, ErrInvalidSession
	}
	hash := HashToken(rawToken)
	user, tokenID, abilities, projectID, err := a.store.APITokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrInvalidSession
		}
		return Principal{}, fmt.Errorf("auth: resolving api token: %w", err)
	}
	_ = a.store.TouchAPIToken(ctx, hash) // best-effort; never blocks the request
	return Principal{
		User: user, Kind: KindAPIToken, Abilities: abilities,
		TokenID: tokenID, ProjectID: projectID,
	}, nil
}
