// Package store is the only writer to PostgreSQL, the single state of record
// (ADR-001/ADR-003). It wraps sqlc-generated queries and confines all pgx and
// pgtype types to this package; the rest of the control plane speaks in
// domain types. Migrations are embedded and applied on boot.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" for database/sql (goose)
	"github.com/pressly/goose/v3"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a lookup matches no row. Callers match it with
// errors.Is, never by string (ENGINEERING rule 3).
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when an insert or update violates a uniqueness
// constraint (a duplicate name within its scope). Handlers map it to 409.
var ErrConflict = errors.New("store: already exists")

// ErrInUse is returned when a delete is refused because other rows still
// reference the target through a RESTRICT foreign key (e.g. a server that
// still runs applications). Handlers map it to 409 with the reason.
var ErrInUse = errors.New("store: still referenced")

// Store is the control plane's persistence layer.
type Store struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: opening pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: pinging database: %w", err)
	}
	return &Store{pool: pool, q: db.New(pool)}, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// Ping verifies the database is reachable, for readiness checks.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Migrate applies all embedded migrations to the database at databaseURL. It
// opens its own database/sql handle because goose operates on that interface;
// the handle is closed before returning.
func Migrate(ctx context.Context, databaseURL string) error {
	sqldb, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("store: opening sql handle for migrations: %w", err)
	}
	defer func() { _ = sqldb.Close() }()

	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: locating migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqldb, sub)
	if err != nil {
		return fmt.Errorf("store: building migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("store: applying migrations: %w", err)
	}
	return nil
}

// ─── Users ──────────────────────────────────────────────────────────────────

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	n, err := s.q.CountUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: counting users: %w", err)
	}
	return n, nil
}

func (s *Store) CreateUser(ctx context.Context, id, email, passwordHash, role string) (domain.User, error) {
	row, err := s.q.CreateUser(ctx, db.CreateUserParams{
		ID:           id,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         role,
	})
	if err != nil {
		return domain.User{}, wrapCreate("creating user", err)
	}
	return userFromRow(row), nil
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		return domain.User{}, wrap("getting user by email", err)
	}
	return userFromRow(row), nil
}

// ─── Control-plane CA ───────────────────────────────────────────────────────

// PlaneCA is the persisted CA material: cert is public, the key is stored
// encrypted with a nonce (threat-model §5.1).
type PlaneCA struct {
	CertPEM      []byte
	EncryptedKey []byte
	KeyNonce     []byte
}

func (s *Store) GetPlaneCA(ctx context.Context) (PlaneCA, error) {
	row, err := s.q.GetPlaneCA(ctx)
	if err != nil {
		return PlaneCA{}, wrap("getting plane CA", err)
	}
	return PlaneCA{CertPEM: row.CertPem, EncryptedKey: row.EncryptedKey, KeyNonce: row.KeyNonce}, nil
}

func (s *Store) InsertPlaneCA(ctx context.Context, ca PlaneCA) error {
	err := s.q.InsertPlaneCA(ctx, db.InsertPlaneCAParams{
		CertPem:      ca.CertPEM,
		EncryptedKey: ca.EncryptedKey,
		KeyNonce:     ca.KeyNonce,
	})
	if err != nil {
		return fmt.Errorf("store: inserting plane CA: %w", err)
	}
	return nil
}

// ─── Servers ────────────────────────────────────────────────────────────────

func (s *Store) CreateServer(ctx context.Context, id, name string) (domain.Server, error) {
	row, err := s.q.CreateServer(ctx, db.CreateServerParams{ID: id, Name: name})
	if err != nil {
		return domain.Server{}, fmt.Errorf("store: creating server: %w", err)
	}
	return serverFromRow(row), nil
}

// CreateServerWithToken creates a server and its first join token in a single
// transaction, so an operator never sees a server that has no way to enroll.
func (s *Store) CreateServerWithToken(ctx context.Context, serverID, name, tokenID string, tokenHash []byte, tokenExpiresAt time.Time) (domain.Server, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Server{}, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	qtx := s.q.WithTx(tx)
	row, err := qtx.CreateServer(ctx, db.CreateServerParams{ID: serverID, Name: name})
	if err != nil {
		return domain.Server{}, fmt.Errorf("store: creating server: %w", err)
	}
	if _, err := qtx.CreateJoinToken(ctx, db.CreateJoinTokenParams{
		ID:        tokenID,
		ServerID:  serverID,
		TokenHash: tokenHash,
		ExpiresAt: tsFromTime(tokenExpiresAt),
	}); err != nil {
		return domain.Server{}, fmt.Errorf("store: creating join token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Server{}, fmt.Errorf("store: committing server creation: %w", err)
	}
	return serverFromRow(row), nil
}

func (s *Store) GetServer(ctx context.Context, id string) (domain.Server, error) {
	row, err := s.q.GetServer(ctx, id)
	if err != nil {
		return domain.Server{}, wrap("getting server", err)
	}
	return serverFromRow(row), nil
}

func (s *Store) ListServers(ctx context.Context) ([]domain.Server, error) {
	rows, err := s.q.ListServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing servers: %w", err)
	}
	out := make([]domain.Server, 0, len(rows))
	for _, r := range rows {
		out = append(out, serverFromRow(r))
	}
	return out, nil
}

func (s *Store) MarkServerEnrolled(ctx context.Context, id, hostname, agentVersion string) (domain.Server, error) {
	row, err := s.q.MarkServerEnrolled(ctx, db.MarkServerEnrolledParams{
		ID:           id,
		Hostname:     hostname,
		AgentVersion: agentVersion,
	})
	if err != nil {
		return domain.Server{}, wrap("marking server enrolled", err)
	}
	return serverFromRow(row), nil
}

func (s *Store) RecordHeartbeat(ctx context.Context, id string, status domain.ServerStatus, agentVersion, driver, role string) (domain.Server, error) {
	row, err := s.q.RecordHeartbeat(ctx, db.RecordHeartbeatParams{
		ID:           id,
		Status:       string(status),
		AgentVersion: agentVersion,
		Driver:       driver,
		Role:         role,
	})
	if err != nil {
		return domain.Server{}, wrap("recording heartbeat", err)
	}
	return serverFromRow(row), nil
}

// MarkStaleServersUnknown flips every enrolled server not seen since cutoff to
// Unknown, returning the IDs it changed. This is how a silently-gone agent
// stops showing a stale Running status (ui-principles §10).
func (s *Store) MarkStaleServersUnknown(ctx context.Context, cutoff time.Time) ([]string, error) {
	ids, err := s.q.MarkStaleServersUnknown(ctx, tsFromTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("store: marking stale servers unknown: %w", err)
	}
	return ids, nil
}

func (s *Store) DeleteServer(ctx context.Context, id string) error {
	if err := s.q.DeleteServer(ctx, id); err != nil {
		return wrapDelete("deleting server", err)
	}
	return nil
}

// AgentEnrolled reports whether id names a server that exists and has
// completed enrollment. It satisfies bus.AgentAuthorizer: the bus refuses
// connections from identities this returns false for (threat-model §8 req 6).
func (s *Store) AgentEnrolled(ctx context.Context, id string) (bool, error) {
	ok, err := s.q.ServerIsEnrolled(ctx, id)
	if err != nil {
		return false, fmt.Errorf("store: checking enrollment of %s: %w", id, err)
	}
	return ok, nil
}

// ─── Join tokens ────────────────────────────────────────────────────────────

func (s *Store) CreateJoinToken(ctx context.Context, id, serverID string, tokenHash []byte, expiresAt time.Time) (domain.JoinToken, error) {
	row, err := s.q.CreateJoinToken(ctx, db.CreateJoinTokenParams{
		ID:        id,
		ServerID:  serverID,
		TokenHash: tokenHash,
		ExpiresAt: tsFromTime(expiresAt),
	})
	if err != nil {
		return domain.JoinToken{}, fmt.Errorf("store: creating join token: %w", err)
	}
	return joinTokenFromRow(row), nil
}

func (s *Store) GetJoinToken(ctx context.Context, id string) (domain.JoinToken, error) {
	row, err := s.q.GetJoinToken(ctx, id)
	if err != nil {
		return domain.JoinToken{}, wrap("getting join token", err)
	}
	return joinTokenFromRow(row), nil
}

// ConsumeJoinToken atomically consumes the token, returning ErrNotFound if it
// was already consumed or has expired (the single-use guarantee, threat-model
// §5.3). Callers must verify the secret hash before calling this.
func (s *Store) ConsumeJoinToken(ctx context.Context, id string) (domain.JoinToken, error) {
	row, err := s.q.ConsumeJoinToken(ctx, id)
	if err != nil {
		return domain.JoinToken{}, wrap("consuming join token", err)
	}
	return joinTokenFromRow(row), nil
}

// ─── Sessions ───────────────────────────────────────────────────────────────

func (s *Store) CreateSession(ctx context.Context, id, userID string, tokenHash []byte, expiresAt time.Time) error {
	_, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:        id,
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: tsFromTime(expiresAt),
	})
	if err != nil {
		return fmt.Errorf("store: creating session: %w", err)
	}
	return nil
}

// UserForSessionToken returns the user owning a live (unexpired) session whose
// token hashes to tokenHash, or ErrNotFound.
func (s *Store) UserForSessionToken(ctx context.Context, tokenHash []byte) (domain.User, error) {
	user, _, err := s.SessionForToken(ctx, tokenHash)
	return user, err
}

// SessionForToken returns the owning user and the session's id in one query.
// Authentication needs both — the id marks "this device" in the session list —
// and the join already selects both rows, so resolving them separately would
// double the query traffic on every authenticated request for nothing.
func (s *Store) SessionForToken(ctx context.Context, tokenHash []byte) (domain.User, string, error) {
	row, err := s.q.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return domain.User{}, "", wrap("getting session", err)
	}
	return userFromRow(row.User), row.Session.ID, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if err := s.q.DeleteSession(ctx, tokenHash); err != nil {
		return fmt.Errorf("store: deleting session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes every session whose expiry is at or before
// the cutoff and reports how many it removed (control-plane-hardening.md §7).
// The cutoff is the caller's clock, not now(), so the purge is deterministic
// under test.
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q.DeleteExpiredSessions(ctx, tsFromTime(before))
	if err != nil {
		return 0, fmt.Errorf("store: deleting expired sessions: %w", err)
	}
	return n, nil
}

// ListSessionsByUser returns a user's live sessions, newest first.
func (s *Store) ListSessionsByUser(ctx context.Context, userID string) ([]domain.Session, error) {
	rows, err := s.q.ListSessionsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("store: listing sessions: %w", err)
	}
	out := make([]domain.Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Session{
			ID:        r.ID,
			UserID:    r.UserID,
			ExpiresAt: r.ExpiresAt.Time,
			CreatedAt: r.CreatedAt.Time,
		})
	}
	return out, nil
}

// DeleteSessionForUser revokes one session, but only if it belongs to userID.
// Reports whether a row was actually removed — a foreign or unknown id removes
// nothing and is indistinguishable to the caller.
func (s *Store) DeleteSessionForUser(ctx context.Context, sessionID, userID string) (bool, error) {
	n, err := s.q.DeleteSessionForUser(ctx, db.DeleteSessionForUserParams{ID: sessionID, UserID: userID})
	if err != nil {
		return false, fmt.Errorf("store: deleting session: %w", err)
	}
	return n > 0, nil
}

// DeleteOtherSessionsForUser revokes every session of a user except the one
// presenting keepTokenHash, returning how many were removed.
func (s *Store) DeleteOtherSessionsForUser(ctx context.Context, userID string, keepTokenHash []byte) (int64, error) {
	n, err := s.q.DeleteOtherSessionsForUser(ctx, db.DeleteOtherSessionsForUserParams{
		UserID: userID, TokenHash: keepTokenHash,
	})
	if err != nil {
		return 0, fmt.Errorf("store: revoking other sessions: %w", err)
	}
	return n, nil
}

// ─── API tokens ─────────────────────────────────────────────────────────────

// CreateAPIToken persists a personal access token (only its hash) and returns
// the stored record.
func (s *Store) CreateAPIToken(ctx context.Context, id, userID, name string, abilities []domain.Ability, tokenHash []byte, expiresAt *time.Time) (domain.APIToken, error) {
	row, err := s.q.CreateAPIToken(ctx, db.CreateAPITokenParams{
		ID:        id,
		UserID:    userID,
		Name:      name,
		TokenHash: tokenHash,
		ExpiresAt: tsFromPtr(expiresAt),
		Abilities: abilityStrings(abilities),
	})
	if err != nil {
		return domain.APIToken{}, wrapCreate("creating api token", err)
	}
	return apiTokenFromRow(row), nil
}

// APITokenByHash returns the user owning a live (unexpired) token whose secret
// hashes to tokenHash, together with the token's id and abilities, or
// ErrNotFound.
func (s *Store) APITokenByHash(ctx context.Context, tokenHash []byte) (domain.User, string, []domain.Ability, error) {
	row, err := s.q.APITokenByHash(ctx, tokenHash)
	if err != nil {
		return domain.User{}, "", nil, wrap("getting api token", err)
	}
	return userFromRow(row.User), row.TokenID, abilitiesFromStrings(row.Abilities), nil
}

// abilityStrings and abilitiesFromStrings convert between the domain vocabulary
// and the text[] column. Unknown stored values are dropped rather than trusted:
// authority must come from the vocabulary this binary knows.
func abilityStrings(in []domain.Ability) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		out = append(out, string(a))
	}
	return out
}

func abilitiesFromStrings(in []string) []domain.Ability {
	out := make([]domain.Ability, 0, len(in))
	for _, s := range in {
		if a := domain.Ability(s); domain.ValidAbility(a) {
			out = append(out, a)
		}
	}
	return out
}

// TouchAPIToken records that a token was just used (best-effort last_used_at).
func (s *Store) TouchAPIToken(ctx context.Context, tokenHash []byte) error {
	if err := s.q.TouchAPIToken(ctx, tokenHash); err != nil {
		return fmt.Errorf("store: touching api token: %w", err)
	}
	return nil
}

// ListAPITokensByUser returns a user's tokens, newest first (never the secret).
func (s *Store) ListAPITokensByUser(ctx context.Context, userID string) ([]domain.APIToken, error) {
	rows, err := s.q.ListAPITokensByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("store: listing api tokens: %w", err)
	}
	out := make([]domain.APIToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.APIToken{
			ID:         r.ID,
			UserID:     r.UserID,
			Name:       r.Name,
			Abilities:  abilitiesFromStrings(r.Abilities),
			LastUsedAt: ptrTime(r.LastUsedAt),
			ExpiresAt:  ptrTime(r.ExpiresAt),
			CreatedAt:  r.CreatedAt.Time,
		})
	}
	return out, nil
}

// GetAPIToken returns a token's metadata by id (for ownership checks on delete).
func (s *Store) GetAPIToken(ctx context.Context, id string) (domain.APIToken, error) {
	r, err := s.q.GetAPIToken(ctx, id)
	if err != nil {
		return domain.APIToken{}, wrap("getting api token by id", err)
	}
	return domain.APIToken{
		ID:         r.ID,
		UserID:     r.UserID,
		Name:       r.Name,
		Abilities:  abilitiesFromStrings(r.Abilities),
		LastUsedAt: ptrTime(r.LastUsedAt),
		ExpiresAt:  ptrTime(r.ExpiresAt),
		CreatedAt:  r.CreatedAt.Time,
	}, nil
}

// DeleteAPIToken revokes a token by id.
func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	if err := s.q.DeleteAPIToken(ctx, id); err != nil {
		return fmt.Errorf("store: deleting api token: %w", err)
	}
	return nil
}

// ─── TOTP two-factor auth ─────────────────────────────────────────────────────

// TOTPSecret is the stored second-factor material for a user.
type TOTPSecret struct {
	CT      []byte
	Nonce   []byte
	Enabled bool
}

// SetUserAvatar replaces the caller's photo. The bytes arrive already validated
// — the store's job is the row, not the policy.
func (s *Store) SetUserAvatar(ctx context.Context, userID, contentType string, data []byte, etag string) error {
	if err := s.q.SetUserAvatar(ctx, db.SetUserAvatarParams{UserID: userID, ContentType: contentType, Bytes: data, Etag: etag}); err != nil {
		return fmt.Errorf("store: setting avatar: %w", err)
	}
	return nil
}

// GetUserAvatar returns a user's photo, or ErrNotFound when they have none.
func (s *Store) GetUserAvatar(ctx context.Context, userID string) (domain.Avatar, error) {
	row, err := s.q.GetUserAvatar(ctx, userID)
	if err != nil {
		return domain.Avatar{}, wrap("getting avatar", err)
	}
	return domain.Avatar{ContentType: row.ContentType, Bytes: row.Bytes, ETag: row.Etag, UpdatedAt: row.UpdatedAt.Time}, nil
}

// DeleteUserAvatar removes the photo; the initials come back in its place.
func (s *Store) DeleteUserAvatar(ctx context.Context, userID string) error {
	if err := s.q.DeleteUserAvatar(ctx, userID); err != nil {
		return fmt.Errorf("store: deleting avatar: %w", err)
	}
	return nil
}

// ─── panel mail (docs/features/panel-mail.md) ───────────────────────────────

// GetPanelMail returns the sealed SMTP configuration, or ErrNotFound when the
// panel has never been given one.
func (s *Store) GetPanelMail(ctx context.Context) (ct, nonce []byte, updatedAt time.Time, err error) {
	row, err := s.q.GetPanelMail(ctx)
	if err != nil {
		return nil, nil, time.Time{}, wrap("getting panel mail", err)
	}
	return row.ConfigCt, row.ConfigNonce, row.UpdatedAt.Time, nil
}

// SetPanelMail replaces the configuration wholesale — there is no partial
// update, for the reason notifiers refuse one: half-writing a credential.
func (s *Store) SetPanelMail(ctx context.Context, ct, nonce []byte) error {
	if err := s.q.SetPanelMail(ctx, db.SetPanelMailParams{ConfigCt: ct, ConfigNonce: nonce}); err != nil {
		return fmt.Errorf("store: setting panel mail: %w", err)
	}
	return nil
}

// DeletePanelMail forgets the configuration; the panel can no longer send.
func (s *Store) DeletePanelMail(ctx context.Context) error {
	if err := s.q.DeletePanelMail(ctx); err != nil {
		return fmt.Errorf("store: deleting panel mail: %w", err)
	}
	return nil
}

// ─── email changes ──────────────────────────────────────────────────────────

// CreateEmailChange records a pending move and the hash of the secret that will
// authorise it.
func (s *Store) CreateEmailChange(ctx context.Context, id, userID, newEmail string, tokenHash []byte, expiresAt time.Time) (domain.EmailChange, error) {
	row, err := s.q.CreateEmailChange(ctx, db.CreateEmailChangeParams{
		ID: id, UserID: userID, NewEmail: newEmail, TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return domain.EmailChange{}, wrapCreate("creating email change", err)
	}
	return emailChangeFromRow(row), nil
}

// EmailChangeTokenHash returns the stored hash for a pending change, so the
// caller can compare the presented secret before spending anything.
func (s *Store) EmailChangeTokenHash(ctx context.Context, id string) (domain.EmailChange, []byte, error) {
	row, err := s.q.GetEmailChange(ctx, id)
	if err != nil {
		return domain.EmailChange{}, nil, wrap("getting email change", err)
	}
	return emailChangeFromRow(row), row.TokenHash, nil
}

// ConsumeEmailChange spends the change. No row back means it was already used or
// has expired — the only race-free answer, which is why it is one statement.
func (s *Store) ConsumeEmailChange(ctx context.Context, id string) (domain.EmailChange, error) {
	row, err := s.q.ConsumeEmailChange(ctx, id)
	if err != nil {
		return domain.EmailChange{}, wrap("consuming email change", err)
	}
	return emailChangeFromRow(row), nil
}

func emailChangeFromRow(r db.EmailChange) domain.EmailChange {
	ec := domain.EmailChange{
		ID: r.ID, UserID: r.UserID, NewEmail: r.NewEmail,
		ExpiresAt: r.ExpiresAt.Time, CreatedAt: r.CreatedAt.Time,
	}
	if r.ConsumedAt.Valid {
		t := r.ConsumedAt.Time
		ec.ConsumedAt = &t
	}
	return ec
}

// GetUserByID loads one account. Used where the caller already holds an id and
// must re-read the row — proving a current password, for instance, where the
// session's cached copy is not enough.
func (s *Store) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	row, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return domain.User{}, wrap("getting user by id", err)
	}
	return userFromRow(row), nil
}

// UpdateUserEmail moves an account to a new sign-in address. The uniqueness
// constraint on users.email is the last word here, so a race between two
// changes resolves in the database rather than in a check-then-write.
func (s *Store) UpdateUserEmail(ctx context.Context, userID, email string) (domain.User, error) {
	row, err := s.q.UpdateUserEmail(ctx, db.UpdateUserEmailParams{ID: userID, Email: email})
	if err != nil {
		return domain.User{}, wrapUpdate("updating email", err)
	}
	return userFromRow(row), nil
}

// UpdateUserProfile writes the fields a person sets about themselves. Both are
// stored verbatim after the caller has validated them: the store's job is the
// row, not the policy.
func (s *Store) UpdateUserProfile(ctx context.Context, userID, displayName, timezone string) (domain.User, error) {
	row, err := s.q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{ID: userID, DisplayName: displayName, Timezone: timezone})
	if err != nil {
		return domain.User{}, wrapUpdate("updating profile", err)
	}
	return userFromRow(row), nil
}

// UpdateUserPassword replaces the stored hash. Revoking the sessions that were
// opened with the old password is the caller's decision, not this one's.
func (s *Store) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	if err := s.q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: userID, PasswordHash: passwordHash}); err != nil {
		return fmt.Errorf("store: updating password: %w", err)
	}
	return nil
}

// SetTOTPSecret stores (or replaces) the enrolling secret; it does not activate
// two-factor — EnableTOTP does, after a code is verified.
func (s *Store) SetTOTPSecret(ctx context.Context, userID string, ct, nonce []byte) error {
	if err := s.q.SetTOTPSecret(ctx, db.SetTOTPSecretParams{ID: userID, TotpSecretEnc: ct, TotpSecretNonce: nonce}); err != nil {
		return fmt.Errorf("store: setting totp secret: %w", err)
	}
	return nil
}

// EnableTOTP activates two-factor for a user (after successful verification).
func (s *Store) EnableTOTP(ctx context.Context, userID string) error {
	if err := s.q.EnableTOTP(ctx, userID); err != nil {
		return fmt.Errorf("store: enabling totp: %w", err)
	}
	return nil
}

// DisableTOTP clears the secret and deactivates two-factor.
func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	if err := s.q.DisableTOTP(ctx, userID); err != nil {
		return fmt.Errorf("store: disabling totp: %w", err)
	}
	return nil
}

// GetTOTPSecret returns a user's stored second-factor material.
func (s *Store) GetTOTPSecret(ctx context.Context, userID string) (TOTPSecret, error) {
	row, err := s.q.GetTOTPSecret(ctx, userID)
	if err != nil {
		return TOTPSecret{}, wrap("getting totp secret", err)
	}
	return TOTPSecret{CT: row.TotpSecretEnc, Nonce: row.TotpSecretNonce, Enabled: row.TotpEnabled}, nil
}

// AddRecoveryCode stores one hashed single-use recovery code.
func (s *Store) AddRecoveryCode(ctx context.Context, id, userID string, codeHash []byte) error {
	if err := s.q.AddRecoveryCode(ctx, db.AddRecoveryCodeParams{ID: id, UserID: userID, CodeHash: codeHash}); err != nil {
		return fmt.Errorf("store: adding recovery code: %w", err)
	}
	return nil
}

// ConsumeRecoveryCode marks the matching unused code used, returning true if a
// code was actually spent (false ⇒ wrong or already-used code).
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID string, codeHash []byte) (bool, error) {
	_, err := s.q.ConsumeRecoveryCode(ctx, db.ConsumeRecoveryCodeParams{UserID: userID, CodeHash: codeHash})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: consuming recovery code: %w", err)
	}
	return true, nil
}

// CountUnusedRecoveryCodes returns how many recovery codes remain.
func (s *Store) CountUnusedRecoveryCodes(ctx context.Context, userID string) (int, error) {
	n, err := s.q.CountUnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("store: counting recovery codes: %w", err)
	}
	return int(n), nil
}

// DeleteRecoveryCodes removes all of a user's recovery codes (on re-enroll or
// disable).
func (s *Store) DeleteRecoveryCodes(ctx context.Context, userID string) error {
	if err := s.q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return fmt.Errorf("store: deleting recovery codes: %w", err)
	}
	return nil
}

// ─── mapping helpers ────────────────────────────────────────────────────────

func wrap(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("store: %s: %w", op, ErrNotFound)
	}
	return fmt.Errorf("store: %s: %w", op, err)
}

// PostgreSQL error codes (Appendix A) matched in wrapCreate/wrapDelete.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// wrapCreate maps constraint violations on inserts: a unique violation is a
// caller-visible conflict; a foreign-key violation means a referenced parent
// vanished between the service's existence check and the insert — not found.
func wrapCreate(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return fmt.Errorf("store: %s: %w", op, ErrConflict)
		case pgForeignKeyViolation:
			return fmt.Errorf("store: %s: %w", op, ErrNotFound)
		}
	}
	return fmt.Errorf("store: %s: %w", op, err)
}

// wrapUpdate maps a unique violation on update (e.g. renaming onto a taken
// name) to ErrConflict, and no-rows to ErrNotFound.
func wrapUpdate(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return fmt.Errorf("store: %s: %w", op, ErrConflict)
	}
	return wrap(op, err)
}

// wrapDelete maps a foreign-key violation on delete — the row is still
// referenced through a RESTRICT constraint — to ErrInUse.
func wrapDelete(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		return fmt.Errorf("store: %s: %w", op, ErrInUse)
	}
	return fmt.Errorf("store: %s: %w", op, err)
}

func serverFromRow(r db.Server) domain.Server {
	return domain.Server{
		ID:            r.ID,
		Name:          r.Name,
		Status:        domain.ServerStatus(r.Status),
		Driver:        r.Driver,
		Role:          r.Role,
		AgentVersion:  r.AgentVersion,
		Hostname:      r.Hostname,
		PublicAddress: r.PublicAddress,
		EnrolledAt:    ptrTime(r.EnrolledAt),
		LastSeenAt:    ptrTime(r.LastSeenAt),
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
}

func userFromRow(r db.User) domain.User {
	return domain.User{
		ID:           r.ID,
		Email:        r.Email,
		PasswordHash: r.PasswordHash,
		Role:         r.Role,
		DisplayName:  r.DisplayName,
		Timezone:     r.Timezone,
		TOTPEnabled:  r.TotpEnabled,
		CreatedAt:    r.CreatedAt.Time,
		UpdatedAt:    r.UpdatedAt.Time,
	}
}

func apiTokenFromRow(r db.ApiToken) domain.APIToken {
	return domain.APIToken{
		ID:         r.ID,
		UserID:     r.UserID,
		Name:       r.Name,
		Abilities:  abilitiesFromStrings(r.Abilities),
		LastUsedAt: ptrTime(r.LastUsedAt),
		ExpiresAt:  ptrTime(r.ExpiresAt),
		CreatedAt:  r.CreatedAt.Time,
	}
}

func joinTokenFromRow(r db.JoinToken) domain.JoinToken {
	return domain.JoinToken{
		ID:         r.ID,
		ServerID:   r.ServerID,
		TokenHash:  r.TokenHash,
		ExpiresAt:  r.ExpiresAt.Time,
		ConsumedAt: ptrTime(r.ConsumedAt),
		CreatedAt:  r.CreatedAt.Time,
	}
}

func ptrTime(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

func tsFromTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
