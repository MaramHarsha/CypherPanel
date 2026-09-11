// Package registries owns container registry credentials (registries.md;
// ADR-008 path 3).
//
// ADR-008's premise is that no registry is required: a single-server build
// keeps its image in the local daemon, and a multi-server one travels over the
// mTLS relay. A registry exists here for the two cases neither covers —
// pulling a private base image, and pushing builds somewhere the operator
// already runs — and nothing in the deploy path may come to depend on one.
package registries

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/egress"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ValidationError marks input the caller can fix; REST maps it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// ErrInUse is returned when a registry cannot be deleted because applications
// still reference it.
var ErrInUse = errors.New("registries: applications still use this registry")

// testTimeout bounds one authentication attempt. A registry that has not
// answered in this long is not one a deploy should be waiting on either.
const testTimeout = 10 * time.Second

// Store is the persistence this needs (consumer-defined, ENGINEERING rule 6).
type Store interface {
	CreateRegistry(ctx context.Context, r domain.Registry) (domain.Registry, error)
	GetRegistry(ctx context.Context, id string) (domain.Registry, error)
	ListRegistriesByTeams(ctx context.Context, teamIDs []string) ([]domain.Registry, error)
	UpdateRegistry(ctx context.Context, id string, f store.UpdateRegistryFields) (domain.Registry, error)
	RecordRegistryTest(ctx context.Context, id string, ok bool, detail string) (domain.Registry, error)
	DeleteRegistry(ctx context.Context, id string) error
	ApplicationsUsingRegistry(ctx context.Context, id string) ([]domain.RegistryUse, error)
}

// SecretBox seals and opens the credential under the master key.
type SecretBox interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	Open(ciphertext, nonce []byte) ([]byte, error)
}

type Service struct {
	store Store
	box   SecretBox
	http  *http.Client
}

func NewService(s Store, box SecretBox) *Service {
	// Guarded at dial time, for every probe this service makes — the stored
	// path as much as the unsaved one. Two policies for "test this credential"
	// would only mean the weaker one is the one people reach for.
	return &Service{store: s, box: box, http: egress.HTTPClient(testTimeout)}
}

// Input is a registry as the operator describes it. Token is plaintext on the
// way in and is sealed before it reaches storage.
type Input struct {
	Name     string
	URL      string
	Username string
	Token    string
	CanPull  bool
	CanPush  bool
}

// Create validates and stores a registry.
func (s *Service) Create(ctx context.Context, teamID string, in Input) (domain.Registry, error) {
	in.Name, in.URL, in.Username = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), strings.TrimSpace(in.Username)
	if err := validate(in, true); err != nil {
		return domain.Registry{}, err
	}
	ct, nonce, err := s.box.Seal([]byte(in.Token))
	if err != nil {
		return domain.Registry{}, fmt.Errorf("registries: sealing token: %w", err)
	}
	return s.store.CreateRegistry(ctx, domain.Registry{
		ID: ids.New(ids.PrefixRegistry), TeamID: teamID,
		Name: in.Name, URL: in.URL, Username: in.Username,
		TokenCT: ct, TokenNonce: nonce,
		CanPull: in.CanPull, CanPush: in.CanPush,
	})
}

// UpdateInput is a partial edit; a nil field is left alone.
type UpdateInput struct {
	Name     *string
	URL      *string
	Username *string
	Token    *string
	CanPull  *bool
	CanPush  *bool
}

func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (domain.Registry, error) {
	current, err := s.store.GetRegistry(ctx, id)
	if err != nil {
		return domain.Registry{}, err
	}
	merged := Input{
		Name: current.Name, URL: current.URL, Username: current.Username,
		CanPull: current.CanPull, CanPush: current.CanPush,
		Token: "(stored)", // stands in for "already set"; only a sent token is checked
	}
	f := store.UpdateRegistryFields{}
	if in.Name != nil {
		merged.Name = strings.TrimSpace(*in.Name)
		f.Name = &merged.Name
	}
	if in.URL != nil {
		merged.URL = strings.TrimSpace(*in.URL)
		f.URL = &merged.URL
	}
	if in.Username != nil {
		merged.Username = strings.TrimSpace(*in.Username)
		f.Username = &merged.Username
	}
	if in.CanPull != nil {
		merged.CanPull = *in.CanPull
		f.CanPull = in.CanPull
	}
	if in.CanPush != nil {
		merged.CanPush = *in.CanPush
		f.CanPush = in.CanPush
	}
	if in.Token != nil {
		merged.Token = *in.Token
	}
	// The merged result is held to the same bar as a create, so a PATCH cannot
	// leave a registry in a state Create would have refused.
	if err := validate(merged, in.Token != nil); err != nil {
		return domain.Registry{}, err
	}
	// Resealed only when sent, so an untouched token keeps its ciphertext.
	if in.Token != nil {
		ct, nonce, err := s.box.Seal([]byte(*in.Token))
		if err != nil {
			return domain.Registry{}, fmt.Errorf("registries: sealing token: %w", err)
		}
		f.TokenCT, f.TokenNonce = ct, nonce
	}
	return s.store.UpdateRegistry(ctx, id, f)
}

func (s *Service) Get(ctx context.Context, id string) (domain.Registry, error) {
	return s.store.GetRegistry(ctx, id)
}

func (s *Service) ListForTeams(ctx context.Context, teamIDs []string) ([]domain.Registry, error) {
	return s.store.ListRegistriesByTeams(ctx, teamIDs)
}

func (s *Service) UsedBy(ctx context.Context, id string) ([]domain.RegistryUse, error) {
	return s.store.ApplicationsUsingRegistry(ctx, id)
}

// Delete removes a registry, refusing while applications still reference it.
// The check is explicit rather than left to the foreign key so the refusal can
// name what is holding it.
func (s *Service) Delete(ctx context.Context, id string) error {
	uses, err := s.store.ApplicationsUsingRegistry(ctx, id)
	if err != nil {
		return err
	}
	if len(uses) > 0 {
		return ErrInUse
	}
	return s.store.DeleteRegistry(ctx, id)
}

// Credential is what the agent needs to authenticate one pull or push. It is
// assembled at publish time and never stored on the agent.
type Credential struct {
	URL      string
	Username string
	Token    string
}

// Credential unseals a registry's token for use in a work item.
func (s *Service) Credential(ctx context.Context, id string) (Credential, error) {
	r, err := s.store.GetRegistry(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	token, err := s.box.Open(r.TokenCT, r.TokenNonce)
	if err != nil {
		return Credential{}, fmt.Errorf("registries: opening token: %w", err)
	}
	return Credential{URL: r.URL, Username: r.Username, Token: string(token)}, nil
}

// TestResult is what an authentication attempt found out.
type TestResult struct {
	OK     bool
	Detail string
}

// TestConfig authenticates against a registry without storing anything, so a
// credential can be proven before it is saved.
func (s *Service) TestConfig(ctx context.Context, in Input) (TestResult, error) {
	in.URL, in.Username = strings.TrimSpace(in.URL), strings.TrimSpace(in.Username)
	if err := validate(in, true); err != nil {
		return TestResult{}, err
	}
	return s.probe(ctx, in.URL, in.Username, in.Token), nil
}

// Test authenticates a stored registry and records the outcome.
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	cred, err := s.Credential(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	res := s.probe(ctx, cred.URL, cred.Username, cred.Token)
	if _, err := s.store.RecordRegistryTest(ctx, id, res.OK, res.Detail); err != nil {
		return res, fmt.Errorf("registries: recording test: %w", err)
	}
	return res, nil
}

// probe asks the registry's v2 API whether the credential is accepted.
//
// `GET /v2/` is the OCI distribution spec's own liveness-and-auth endpoint: 200
// means authenticated, 401 means the credential was rejected. It is used rather
// than the catalog endpoint because many registries disable catalog listing for
// non-admin credentials, and a working credential must not look broken because
// it cannot enumerate every repository on the host.
//
// ── Why this is the panel's most careful outbound request ──────────────────
//
// The destination is chosen by the caller, and unlike a notifier's webhook it
// cannot be constrained to a known set of hosts: pointing at an arbitrary
// registry IS the feature. So the two halves of a request-forgery defence are
// both here, and neither substitutes for the other (threat-model §5.11).
//
// FIRST, the URL is built from validated COMPONENTS, never by concatenating the
// operator's string. `hostPort` proves the value is a bare host and optional
// port and nothing else, so a "url" of `evil.test/../../admin`,
// `user@evil.test`, `evil.test#`, or one carrying its own scheme or query
// cannot steer the request anywhere but that host's /v2/.
//
// SECOND, the connection is guarded at dial time by core/egress, which refuses
// any address that is not publicly routable. That is what stops the request
// reaching 169.254.169.254, the plane's own loopback, or an RFC1918 host —
// and it checks the RESOLVED IP, so a name that answers publicly once and
// privately the next time is refused the next time.
//
// The cost is honest and small: an operator's registry on a private network
// cannot be TESTED from the panel. It can still be stored, attached and pulled
// from, because the agent does the pulling and the agent is already on that
// network. The refusal says exactly that rather than reporting a fake
// connection error.
func (s *Service) probe(ctx context.Context, host, username, token string) TestResult {
	// Only the host answers /v2/; a namespace is part of the image path.
	base, ok := hostPort(host)
	if !ok {
		return TestResult{Detail: "that is not a registry host — use a host such as ghcr.io or registry.example.com:5000"}
	}
	// Built from components, so the operator's value can only ever be the URL's
	// Host. Always https: the guard below refuses every address the Docker
	// daemon would have treated as insecure-by-default anyway, so there is no
	// remaining case in which a credential would go out in the clear.
	u := &url.URL{Scheme: "https", Host: base, Path: "/v2/"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return TestResult{Detail: "could not build the request"}
	}
	if username != "" {
		req.SetBasicAuth(username, token)
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		if errors.Is(err, egress.ErrPrivateDestination) {
			return TestResult{Detail: "the panel will not connect to addresses inside its own network — " +
				"save the registry anyway, the agent that pulls is already on that network"}
		}
		// The registry's own words, minus the URL: a token-bearing URL must not
		// ride out in a *url.Error (rule 20).
		var ue *url.Error
		if errors.As(err, &ue) {
			return TestResult{Detail: ue.Err.Error()}
		}
		return TestResult{Detail: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return TestResult{OK: true, Detail: "Authenticated to " + base + "."}
	case http.StatusUnauthorized, http.StatusForbidden:
		return TestResult{Detail: "the registry rejected these credentials"}
	default:
		return TestResult{Detail: "the registry answered " + resp.Status}
	}
}

// registryHost is the complete grammar of a registry reference's host part:
// a DNS name (which covers an IPv4 literal) or a bracketed IPv6 literal, with
// an optional port. `\A`/`\z` rather than `^`/`$` so nothing can hide behind a
// trailing newline.
//
// It is a whitelist expressed in one place. Everything a request-forgery would
// need — a scheme, credentials before an `@`, a path, a query, a fragment, a
// backslash, whitespace, a control character — is simply outside the alphabet,
// so there is no escaping or stripping to get subtly wrong, and the rule can be
// read and checked at a glance rather than traced through a parser.
var registryHost = regexp.MustCompile(`\A(?:` +
	// DNS labels: alphanumeric ends, hyphens only in the middle.
	`[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?` +
	`(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*` +
	// or a bracketed IPv6 literal.
	`|\[[0-9A-Fa-f:.]{2,45}\]` +
	`)(?::[0-9]{1,5})?\z`)

// maxHostLen is the DNS maximum, plus room for ":65535". A longer reference is
// not a host whatever it looks like.
const maxHostLen = 253 + 6

// hostPort reduces a registry reference to the `host[:port]` that answers /v2/,
// and reports whether what it found is one.
//
// A reference may carry a namespace (`ghcr.io/acme`), which belongs to the
// image path and is dropped here — the host alone answers /v2/. What remains is
// then held to registryHost in full, and it is that CHECKED value which is
// returned, so nothing unchecked can reach the URL.
func hostPort(ref string) (string, bool) {
	host := ref
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if len(host) > maxHostLen || !registryHost.MatchString(host) {
		return "", false
	}
	// The grammar allows five digits; only 1–65535 is a port.
	if _, port, err := net.SplitHostPort(host); err == nil {
		if n, cerr := strconv.Atoi(port); cerr != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	return host, true
}

func validate(in Input, tokenRequired bool) error {
	if in.Name == "" || len(in.Name) > 100 {
		return invalid("name must be 1–100 characters")
	}
	if in.URL == "" {
		return invalid("url is required — a registry host such as ghcr.io or registry.example.com")
	}
	// A registry reference carries no scheme; accepting one would produce image
	// names nothing can pull.
	if strings.Contains(in.URL, "://") {
		return invalid("url must be a host, not a URL — drop the https:// prefix")
	}
	if strings.ContainsAny(in.URL, " \t\r\n") {
		return invalid("url must not contain whitespace")
	}
	if tokenRequired && in.Token == "" {
		return invalid("token is required")
	}
	if !in.CanPull && !in.CanPush {
		return invalid("a registry must allow at least one of pull or push")
	}
	return nil
}
