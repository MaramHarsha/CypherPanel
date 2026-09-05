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
	"net/http"
	"net/url"
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
	return &Service{store: s, box: box, http: &http.Client{Timeout: testTimeout}}
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
func (s *Service) probe(ctx context.Context, host, username, token string) TestResult {
	// Only the host answers /v2/; a namespace is part of the image path.
	base := host
	if i := strings.Index(base, "/"); i >= 0 {
		base = base[:i]
	}
	scheme := "https://"
	if isPlainHTTPHost(base) {
		scheme = "http://"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+base+"/v2/", nil)
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

// isPlainHTTPHost reports whether a host is one the Docker daemon itself treats
// as insecure-by-default: localhost and 127.0.0.0/8. Everything else is
// contacted over TLS, because a credential must not be put on the wire in the
// clear to a host nobody has vouched for.
func isPlainHTTPHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1" || strings.HasPrefix(h, "127.")
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
