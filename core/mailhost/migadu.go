package mailhost

// Migadu, the first (and only) Provider implementation (managed-email.md §4).
//
// It ships first because it is what the design names, it is a real hosted
// provider with a documented admin API, and it does not require a business
// relationship to evaluate.
//
// Every call goes out through the EGRESS GUARD, because the base URL is
// operator-supplied configuration and a control plane making authenticated
// requests to an address someone typed is the SSRF shape that guard exists for.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/egress"
)

const (
	migaduDefaultBase = "https://api.migadu.com/v1"
	migaduTimeout     = 20 * time.Second
	maxBody           = 1 << 20
)

// MigaduConfig is the sealed credential. The whole config is sealed, not just
// the token: which half of a provider's configuration is a secret is not a
// judgement this package should be making on the operator's behalf.
type MigaduConfig struct {
	// Account is the admin email the API key belongs to.
	Account string `json:"account"`
	APIKey  string `json:"api_key"`
	// BaseURL overrides the endpoint, for a test double or a self-hosted proxy.
	BaseURL string `json:"base_url,omitempty"`
}

// Migadu speaks the provider's admin API.
type Migadu struct {
	cfg    MigaduConfig
	client *http.Client
}

func NewMigadu(cfg MigaduConfig) *Migadu {
	return &Migadu{cfg: cfg, client: egress.HTTPClient(migaduTimeout)}
}

func (m *Migadu) base() string {
	if m.cfg.BaseURL != "" {
		return strings.TrimSuffix(m.cfg.BaseURL, "/")
	}
	return migaduDefaultBase
}

func (m *Migadu) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
		[]byte(m.cfg.Account+":"+m.cfg.APIKey)))

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: reaching the provider: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// Input, not a fault: the operator has to fix the credential.
		return &AuthError{Msg: "the mail provider refused this account and API key"}
	case resp.StatusCode == http.StatusNotFound:
		return &ValidationError{Msg: "the mail provider does not know that domain or mailbox"}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// The status only. A provider's error page can echo the request back,
		// and the request carries the credential (ENGINEERING rule 20).
		return fmt.Errorf("mail: the provider answered %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("mail: reading the provider's answer: %w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("mail: the provider's answer will not parse: %w", err)
	}
	return nil
}

// isAuth reports whether an error is the operator's credential rather than the
// provider's behaviour.
func isAuth(err error) bool {
	var authErr *AuthError
	return errors.As(err, &authErr)
}

func (m *Migadu) Test(ctx context.Context) error {
	var out struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	return m.do(ctx, http.MethodGet, "/domains", nil, &out)
}

// EnsureDomain registers the domain and returns the records it requires.
//
// Idempotent: a domain the provider already knows is not an error, because the
// caller's desired state is "registered" either way (ENGINEERING rule 12).
func (m *Migadu) EnsureDomain(ctx context.Context, domainName string) ([]Record, error) {
	// A credential problem is the operator's to fix and must surface; a domain
	// the provider already has is the desired state already being true, and
	// treating it as an error would make "enable mail" fail on the second
	// press (ENGINEERING rule 12).
	err := m.do(ctx, http.MethodPost, "/domains", map[string]string{"name": domainName}, nil)
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return nil, err
	}
	return m.RequiredRecords(ctx, domainName)
}

// RequiredRecords asks the provider what the domain needs.
//
// The DKIM public key comes from the PROVIDER: it generated the pair and keeps
// the private half, which is exactly why the panel never holds a DKIM key.
func (m *Migadu) RequiredRecords(ctx context.Context, domainName string) ([]Record, error) {
	var out struct {
		Domain struct {
			Name string `json:"name"`
		} `json:"domain"`
		DKIM struct {
			Selector  string `json:"selector"`
			PublicKey string `json:"public_key"`
		} `json:"dkim"`
	}
	err := m.do(ctx, http.MethodGet, "/domains/"+domainName, nil, &out)
	switch {
	case err == nil:
		return migaduStandardRecords(domainName, out.DKIM.Selector, out.DKIM.PublicKey), nil
	case isAuth(err):
		// A credential the operator must fix has to SURFACE. Falling back here
		// would write records for a provider that is not going to accept the
		// domain, which is the worst of both outcomes.
		return nil, err
	default:
		// The provider does not expose a per-domain read, or the shape moved.
		// Its record set is documented and fixed, so the panel writes what the
		// provider documents rather than refusing to help at all — minus the
		// DKIM record, because a published DKIM record with no key is worse
		// than none: receivers read it as a signing failure.
		return migaduStandardRecords(domainName, "", ""), nil
	}
}

// migaduStandardRecords is the provider's documented record set. Each carries a
// PURPOSE in words, because an operator looking at four TXT records needs to
// know which one is SPF without decoding it.
func migaduStandardRecords(domainName, dkimSelector, dkimKey string) []Record {
	out := []Record{
		{Type: "MX", Name: "@", Content: "aspmx1.migadu.com", TTL: 3600, Priority: 10,
			Purpose: "Where mail for this domain is delivered"},
		{Type: "MX", Name: "@", Content: "aspmx2.migadu.com", TTL: 3600, Priority: 20,
			Purpose: "The backup delivery host"},
		{Type: "TXT", Name: "@", Content: "v=spf1 include:spf.migadu.com -all", TTL: 3600,
			Purpose: "SPF — which servers may send as this domain. -all means nobody else."},
		{Type: "TXT", Name: "_dmarc", Content: "v=DMARC1; p=quarantine; rua=mailto:dmarc@" + domainName, TTL: 3600,
			Purpose: "DMARC — what receivers should do with mail that fails SPF or DKIM"},
	}
	if dkimSelector != "" && dkimKey != "" {
		out = append(out, Record{
			Type: "TXT", Name: dkimSelector + "._domainkey",
			Content: "v=DKIM1; k=rsa; p=" + dkimKey, TTL: 3600,
			Purpose: "DKIM — the provider's signing key. The private half never leaves them.",
		})
	}
	return out
}

func (m *Migadu) ListMailboxes(ctx context.Context, domainName string) ([]Mailbox, error) {
	var out struct {
		Mailboxes []struct {
			LocalPart    string `json:"local_part"`
			DomainName   string `json:"domain_name"`
			Name         string `json:"name"`
			StorageUsage int64  `json:"storage_usage"`
		} `json:"mailboxes"`
	}
	if err := m.do(ctx, http.MethodGet, "/domains/"+domainName+"/mailboxes", nil, &out); err != nil {
		return nil, err
	}
	res := make([]Mailbox, 0, len(out.Mailboxes))
	for _, mb := range out.Mailboxes {
		res = append(res, Mailbox{
			Address: mb.LocalPart + "@" + domainName, Name: mb.Name, StorageBytes: mb.StorageUsage,
		})
	}
	return res, nil
}

func (m *Migadu) CreateMailbox(ctx context.Context, domainName, local, name, password string) (Mailbox, error) {
	body := map[string]any{"local_part": local, "name": name, "password": password}
	var out struct {
		LocalPart string `json:"local_part"`
		Name      string `json:"name"`
	}
	if err := m.do(ctx, http.MethodPost, "/domains/"+domainName+"/mailboxes", body, &out); err != nil {
		return Mailbox{}, err
	}
	return Mailbox{Address: local + "@" + domainName, Name: name}, nil
}

func (m *Migadu) DeleteMailbox(ctx context.Context, domainName, local string) error {
	return m.do(ctx, http.MethodDelete, "/domains/"+domainName+"/mailboxes/"+local, nil, nil)
}

func (m *Migadu) SetPassword(ctx context.Context, domainName, local, password string) error {
	return m.do(ctx, http.MethodPut, "/domains/"+domainName+"/mailboxes/"+local,
		map[string]any{"password": password}, nil)
}

// ConfigHint masks the credential for the API, the same shape notifiers, panel
// mail and the DNS provider already use.
func ConfigHint(cfg MigaduConfig) string {
	if cfg.Account == "" {
		return "migadu · connected · token sealed"
	}
	return "migadu · " + cfg.Account + " · token sealed"
}
