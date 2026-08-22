// Package dns owns the panel's DNS Provider: one connection that both proves an
// operator owns a domain and writes the records that make it resolve
// (docs/features/dns-automation.md).
//
// Two things live here and they are deliberately the same feature. Verification
// asks "is this domain inside a Zone our credential can see?", which is
// ownership by possession of the token. Reconciliation makes the DNS Record
// match desired state, including deleting it when the application is gone.
// Splitting them would let a panel verify a domain it cannot write to, which is
// a promise it could not keep.
package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ValidationError marks input the caller can fix; REST maps it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// ErrNotConfigured is returned when the panel has no DNS Provider. Callers
// surface it as "nothing is enforced", never as a failure: an install with no
// provider behaves exactly as it did before this feature (spec §4.1).
var ErrNotConfigured = errors.New("dns: the panel has no DNS provider configured")

// KindCloudflare is the only provider kind at v1.
const KindCloudflare = "cloudflare"

// recordTTL of 1 means "automatic" to Cloudflare. Automatic is right: the panel
// has no opinion about caching, and a low fixed TTL would be a guess.
const recordTTL = 1

// Config is the provider's credentials. Sealed as one blob, so the field names
// are the wire format and a typo cannot silently become an empty setting.
type Config struct {
	APIToken string `json:"api_token"`
	// AccountID scopes an ACCOUNT-OWNED token to the account that owns it
	// (https://developers.cloudflare.com/fundamentals/api/get-started/account-owned-tokens/).
	//
	// Empty means a user-owned token, which is the classic "My Profile → API
	// Tokens" kind and needs no account. Cloudflare recommends account-owned
	// tokens for durable integrations precisely because they survive the person
	// who made them leaving, which is what a panel is.
	//
	// It changes two calls: zones are listed filtered by account.id, and the
	// token verifies at /accounts/{id}/tokens/verify rather than
	// /user/tokens/verify.
	AccountID string `json:"account_id"`
	// AccountName is cached for display only. It is not a credential and not
	// authoritative — the ID is.
	AccountName string `json:"account_name"`
}

// Store is the persistence this needs (consumer-defined, ENGINEERING rule 6).
type Store interface {
	GetDNSProvider(ctx context.Context) (kind string, ct, nonce []byte, updatedAt time.Time, err error)
	SetDNSProvider(ctx context.Context, kind string, ct, nonce []byte) error
	DeleteDNSProvider(ctx context.Context) error

	ListDNSZones(ctx context.Context) ([]domain.DNSZone, error)
	UpsertDNSZone(ctx context.Context, z domain.DNSZone) (domain.DNSZone, error)
	DeleteDNSZonesNotIn(ctx context.Context, names []string) error

	GetDNSRecordByApplication(ctx context.Context, appID string) (domain.DNSRecord, error)
	UpsertDNSRecord(ctx context.Context, r domain.DNSRecord) (domain.DNSRecord, error)
	TombstoneDNSRecordsForApplication(ctx context.Context, appID string) error
	TombstoneOrphanedDNSRecords(ctx context.Context) error
	ListApplicationsWantingDNS(ctx context.Context) ([]domain.DNSWant, error)
	ListDueDNSRecords(ctx context.Context, now time.Time, limit int32) ([]domain.DNSRecordWithZone, error)
	MarkDNSRecordCreated(ctx context.Context, id, providerRecordID string) (domain.DNSRecord, error)
	DeleteDNSRecordRow(ctx context.Context, id string) error
	FailDNSRecord(ctx context.Context, id, lastErr string, attempt int, next *time.Time) error
}

// SecretBox seals and opens the configuration under the master key.
type SecretBox interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// clientFor builds a provider client for a token. A field so tests can supply a
// fake without a network.
type clientFor func(token string) Client

type Service struct {
	store  Store
	box    SecretBox
	newCli clientFor
	now    func() time.Time
}

func New(st Store, box SecretBox) *Service {
	return &Service{store: st, box: box, newCli: NewCloudflare, now: time.Now}
}

// Settings is what the API may say about the configuration: whether one exists
// and a hint naming it. Never the token.
type Settings struct {
	Configured  bool
	Kind        string
	Hint        string
	ZoneCount   int
	AccountID   string
	AccountName string
	UpdatedAt   time.Time
}

// AmbiguousAccountError is returned when a token can see several accounts and
// the operator has not said which one to use. It carries the choices so the UI
// can render a picker rather than asking them to go and find an ID.
type AmbiguousAccountError struct{ Accounts []Account }

func (e *AmbiguousAccountError) Error() string {
	return "this token can see more than one Cloudflare account — choose which one CypherPanel should manage"
}

// Hint renders the non-secret half. There is nothing in a DNS token worth
// showing, so the hint names what it can REACH — the zones — which is the thing
// an operator actually wants confirmed.
func Hint(zones []domain.DNSZone) string {
	switch len(zones) {
	case 0:
		return "no zones visible to this token"
	case 1:
		return zones[0].Name
	case 2:
		return zones[0].Name + " and " + zones[1].Name
	default:
		return fmt.Sprintf("%s, %s and %d more", zones[0].Name, zones[1].Name, len(zones)-2)
	}
}

// ─── Provider configuration ─────────────────────────────────────────────────

func (s *Service) Get(ctx context.Context) (Settings, error) {
	kind, cfg, updated, err := s.load(ctx)
	// load has already translated a missing row into ErrNotConfigured; matching
	// store.ErrNotFound here never fired, so "no provider connected" — the
	// ordinary state of every install that has not set one up — surfaced as a
	// 500 instead of `configured: false`.
	if errors.Is(err, ErrNotConfigured) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	zones, err := s.store.ListDNSZones(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("dns: listing zones: %w", err)
	}
	return Settings{
		Configured: true, Kind: kind, Hint: Hint(zones), ZoneCount: len(zones),
		AccountID: cfg.AccountID, AccountName: cfg.AccountName, UpdatedAt: updated,
	}, nil
}

// Set validates the credential against the provider BEFORE persisting anything,
// then caches the zones it can see. A token that cannot list zones is refused
// with the provider's own message: a credential that fails at first use is a
// dead end, and a dead end is a bug (ui-principles §11, spec §5.1).
func (s *Service) Set(ctx context.Context, c Config) (Settings, error) {
	if strings.TrimSpace(c.APIToken) == "" {
		return Settings{}, invalid("an API token is required")
	}
	cli := s.newCli(c.APIToken)

	// Resolve which account this token belongs to, unless the operator already
	// said. An account-owned token is scoped to exactly one account, so the
	// common case needs no input at all — asking someone to go and copy an ID
	// they will find in one place is work the panel can do for them.
	if c.AccountID == "" {
		accounts, err := cli.ListAccounts(ctx)
		switch {
		case err != nil:
			// A token without Account:Read cannot list accounts. That is not a
			// failure: a user-owned token does not need an account at all, and
			// an account-owned token whose id the operator pasted is handled
			// above. Fall through unscoped.
		case len(accounts) == 1:
			c.AccountID, c.AccountName = accounts[0].ID, accounts[0].Name
		case len(accounts) > 1:
			return Settings{}, &AmbiguousAccountError{Accounts: accounts}
		}
	} else if accounts, err := cli.ListAccounts(ctx); err == nil {
		// Name the chosen account for display, and refuse one the token cannot
		// actually see — a mistyped id would otherwise produce an empty zone
		// list with no explanation.
		known := false
		for _, a := range accounts {
			if a.ID == c.AccountID {
				c.AccountName, known = a.Name, true
			}
		}
		if !known {
			return Settings{}, invalid("this token cannot see the account " + c.AccountID + " — check the account ID, or leave it empty to let the panel resolve it")
		}
	}

	zones, err := cli.ListZones(ctx, c.AccountID)
	if err != nil {
		var ae *AuthError
		if errors.As(err, &ae) {
			return Settings{}, invalid("Cloudflare rejected this token: " + ae.Msg + " — it needs Zone:Read and DNS:Edit")
		}
		return Settings{}, invalid("could not reach Cloudflare with this token: " + err.Error())
	}
	if len(zones) == 0 {
		// Two very different causes, and the operator can only act on the right
		// one: either the account genuinely has no domains yet, or the token is
		// scoped away from the ones it has. Name both rather than guessing.
		where := "this Cloudflare account"
		if c.AccountID != "" {
			where = accountLabel(c)
		}
		return Settings{}, invalid("no zones found in " + where +
			" — add your domain in Cloudflare first, or check the token's Zone Resources include it. " +
			"A domain that is still pending nameserver setup counts; one that has not been added at all does not")
	}

	blob, err := json.Marshal(c)
	if err != nil {
		return Settings{}, fmt.Errorf("dns: encoding config: %w", err)
	}
	ct, nonce, err := s.box.Seal(blob)
	if err != nil {
		return Settings{}, fmt.Errorf("dns: sealing config: %w", err)
	}
	if err := s.store.SetDNSProvider(ctx, KindCloudflare, ct, nonce); err != nil {
		return Settings{}, fmt.Errorf("dns: saving provider: %w", err)
	}
	if err := s.cacheZones(ctx, zones); err != nil {
		return Settings{}, err
	}
	return s.Get(ctx)
}

// accountLabel names an account the way an operator recognises it.
func accountLabel(c Config) string {
	if c.AccountName != "" {
		return c.AccountName
	}
	return "account " + c.AccountID
}

// Delete disconnects the provider. It deletes NOTHING at Cloudflare: removing
// the token removes our ability to act, not our obligation to be careful
// (spec §4.5). Zones go, because the cache is meaningless without a credential
// to refresh it — and with them every domain's verification, which fails
// closed.
func (s *Service) Delete(ctx context.Context) error {
	if err := s.store.DeleteDNSZonesNotIn(ctx, nil); err != nil {
		return fmt.Errorf("dns: clearing zone cache: %w", err)
	}
	if err := s.store.DeleteDNSProvider(ctx); err != nil {
		return fmt.Errorf("dns: deleting provider: %w", err)
	}
	return nil
}

// RefreshZones re-reads the provider's zone list into the cache.
func (s *Service) RefreshZones(ctx context.Context) ([]domain.DNSZone, error) {
	_, cfg, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := s.newCli(cfg.APIToken).ListZones(ctx, cfg.AccountID)
	if err != nil {
		return nil, err
	}
	if err := s.cacheZones(ctx, zones); err != nil {
		return nil, err
	}
	return s.store.ListDNSZones(ctx)
}

// Test proves the credential still works, without writing anything. It is a
// wiring check, not a state change — a test that wrote state would be a second
// way to configure the panel (the rule panel-mail.md §2.2 set).
func (s *Service) Test(ctx context.Context) error {
	_, cfg, _, err := s.load(ctx)
	if err != nil {
		return err
	}
	// Verify, not list: the verify endpoint is what Cloudflare provides for
	// exactly this question, and it differs by token type — an account-owned
	// token verifies under its account, a user-owned one under /user.
	return s.newCli(cfg.APIToken).VerifyToken(ctx, cfg.AccountID)
}

func (s *Service) cacheZones(ctx context.Context, zones []Zone) error {
	names := make([]string, 0, len(zones))
	for _, z := range zones {
		if _, err := s.store.UpsertDNSZone(ctx, domain.DNSZone{
			ID: ids.New(ids.PrefixDNSZone), ProviderZoneID: z.ProviderID, Name: z.Name, Status: z.Status,
		}); err != nil {
			return fmt.Errorf("dns: caching zone %s: %w", z.Name, err)
		}
		names = append(names, z.Name)
	}
	// Anything the provider no longer lists is no longer ours to manage. A zone
	// that still holds managed records is protected by ON DELETE RESTRICT, so
	// this fails loudly rather than silently orphaning them.
	if err := s.store.DeleteDNSZonesNotIn(ctx, names); err != nil {
		return fmt.Errorf("dns: pruning zone cache: %w", err)
	}
	return nil
}

func (s *Service) load(ctx context.Context) (string, Config, time.Time, error) {
	kind, ct, nonce, updated, err := s.store.GetDNSProvider(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return "", Config{}, time.Time{}, ErrNotConfigured
	}
	if err != nil {
		return "", Config{}, time.Time{}, fmt.Errorf("dns: loading provider: %w", err)
	}
	blob, err := s.box.Open(ct, nonce)
	if err != nil {
		return "", Config{}, time.Time{}, fmt.Errorf("dns: unsealing provider config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(blob, &c); err != nil {
		return "", Config{}, time.Time{}, fmt.Errorf("dns: decoding provider config: %w", err)
	}
	return kind, c, updated, nil
}

// ─── Verification ───────────────────────────────────────────────────────────

// Verification is what the API and the spec builder both ask for. It is
// DERIVED on every call, never stored: a stored flag would survive the token
// being revoked or a zone being removed, and a stale security decision is worse
// than a recomputed one (spec §4.1, threat-model §5.12).
type Verification struct {
	// Enforced is false when no provider is configured. Everything downstream
	// keys off this: no provider means today's behaviour, unchanged.
	Enforced bool
	Verified bool
	// Zone is the matched zone name when Verified.
	Zone string
	// ZoneStatus is that zone's activation state. Verified and non-active is a
	// real, common combination — you own the domain, it just does not resolve
	// through Cloudflare yet.
	ZoneStatus string
	// AvailableZones is what the token CAN see, so a failure can say "here is
	// what you do have" instead of just "no" (ui-principles §11).
	AvailableZones []string
}

// Verify reports whether host falls inside a zone this panel can manage.
//
// An empty host is "verified" in the sense that nothing is being claimed — an
// application with no domain is not published anywhere, so there is nothing to
// own. That keeps the gate in buildSpec from having to special-case it.
func (s *Service) Verify(ctx context.Context, host string) (Verification, error) {
	if _, _, _, _, err := s.store.GetDNSProvider(ctx); errors.Is(err, store.ErrNotFound) {
		return Verification{Enforced: false, Verified: true}, nil
	} else if err != nil {
		return Verification{}, fmt.Errorf("dns: loading provider: %w", err)
	}
	zones, err := s.store.ListDNSZones(ctx)
	if err != nil {
		return Verification{}, fmt.Errorf("dns: listing zones: %w", err)
	}
	v := Verification{Enforced: true, AvailableZones: zoneNames(zones)}
	if strings.TrimSpace(host) == "" {
		v.Verified = true
		return v, nil
	}
	if z, ok := MatchZone(host, zones); ok {
		v.Verified, v.Zone, v.ZoneStatus = true, z.Name, z.Status
	}
	return v, nil
}

func zoneNames(zones []domain.DNSZone) []string {
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		out = append(out, z.Name)
	}
	return out
}

// MatchZone finds the zone a hostname belongs to, LONGEST SUFFIX WINS:
// api.staging.example.com matches staging.example.com over example.com when
// both are connected, because the more specific zone is the one that actually
// holds the record.
//
// The match is on label boundaries, never raw suffix: "notexample.com" must not
// match zone "example.com", and that distinction is the whole security value of
// this function.
func MatchZone(host string, zones []domain.DNSZone) (domain.DNSZone, bool) {
	h := normalizeHost(host)
	if h == "" {
		return domain.DNSZone{}, false
	}
	var best domain.DNSZone
	found := false
	for _, z := range zones {
		n := normalizeHost(z.Name)
		if n == "" {
			continue
		}
		if h == n || strings.HasSuffix(h, "."+n) {
			if !found || len(n) > len(best.Name) {
				best, found = z, true
			}
		}
	}
	return best, found
}

// normalizeHost lowercases, trims a trailing dot, and drops a port if one was
// pasted in. A domain field should hold a bare hostname, but operators paste.
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i+1:], ".") {
		h = h[:i]
	}
	return h
}
