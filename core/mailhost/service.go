package mailhost

// CRUD and orchestration (managed-email.md §§4, 5).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence this needs (consumer-defined).
type Store interface {
	GetMailProvider(ctx context.Context) (kind string, ct, nonce []byte, err error)
	SetMailProvider(ctx context.Context, kind string, ct, nonce []byte) error
	DeleteMailProvider(ctx context.Context) error
	CreateMailDomain(ctx context.Context, id, name string) (domain.MailDomain, error)
	GetMailDomain(ctx context.Context, id string) (domain.MailDomain, error)
	ListMailDomains(ctx context.Context) ([]domain.MailDomain, error)
	SetMailDomainRecords(ctx context.Context, id string, at *time.Time, lastError string) error
	DeleteMailDomain(ctx context.Context, id string) error
	CreateMailboxLink(ctx context.Context, l domain.MailboxLink) (domain.MailboxLink, error)
	ListMailboxLinks(ctx context.Context, domainID string) ([]domain.MailboxLink, error)
	DeleteMailboxLink(ctx context.Context, address string) error
}

// SecretBox seals the provider credential.
type SecretBox interface {
	Seal(plaintext []byte) (ct, nonce []byte, err error)
	Open(ct, nonce []byte) ([]byte, error)
}

// DNSWriter writes the provider's required records through the DNS automation
// that is already connected. This is deliberately the SAME path an
// application's domain follows, not a second one.
type DNSWriter interface {
	// EnsureRecord writes one record and reports whether it is live.
	EnsureRecord(ctx context.Context, zoneDomain string, r Record) error
	// Verified reports whether the panel can manage records for this domain at
	// all. Enabling mail on a domain the panel cannot manage would leave the
	// feature printing instructions, which is not what the screen promises.
	Verified(ctx context.Context, domainName string) (bool, error)
}

// Service is phases 1 and 2.
type Service struct {
	store Store
	box   SecretBox
	dns   DNSWriter
	// newProvider builds a client from a config. A field so tests supply a fake
	// without a network.
	newProvider func(cfg MigaduConfig) Provider
	log         *slog.Logger
}

// SetLogger attaches a logger, so a link that could not be written is visible
// rather than only implied by an empty field.
func (s *Service) SetLogger(l *slog.Logger) { s.log = l }

func (s *Service) linkFailed(address string, err error) {
	if s.log == nil {
		return
	}
	s.log.Error("mailhost: the mailbox was created but its panel link was not",
		"address", address, "error", err)
}

func NewService(store Store, box SecretBox, dns DNSWriter) *Service {
	return &Service{
		store: store, box: box, dns: dns,
		newProvider: func(cfg MigaduConfig) Provider { return NewMigadu(cfg) },
	}
}

// Connected reports whether a provider credential is stored, and its hint.
func (s *Service) Connected(ctx context.Context) (bool, string) {
	kind, ct, nonce, err := s.store.GetMailProvider(ctx)
	if err != nil {
		return false, ""
	}
	raw, err := s.box.Open(ct, nonce)
	if err != nil {
		return true, kind + " · connected · token sealed"
	}
	var cfg MigaduConfig
	_ = json.Unmarshal(raw, &cfg)
	return true, ConfigHint(cfg)
}

// Connect seals the credential after proving it works. Testing before storing
// is the point: a credential saved and then found broken is one an operator
// discovers when a mailbox creation fails, which is the wrong moment.
func (s *Service) Connect(ctx context.Context, cfg MigaduConfig) error {
	if cfg.Account == "" || cfg.APIKey == "" {
		return invalid("the provider needs an admin account and an API key")
	}
	if err := s.newProvider(cfg).Test(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	ct, nonce, err := s.box.Seal(raw)
	if err != nil {
		return fmt.Errorf("mailhost: sealing the credential: %w", err)
	}
	return s.store.SetMailProvider(ctx, "migadu", ct, nonce)
}

// Disconnect forgets the credential. Domains and mailboxes at the provider are
// untouched — disconnecting the panel is not the same decision as deleting
// somebody's mail.
func (s *Service) Disconnect(ctx context.Context) error {
	return s.store.DeleteMailProvider(ctx)
}

func (s *Service) provider(ctx context.Context) (Provider, error) {
	_, ct, nonce, err := s.store.GetMailProvider(ctx)
	if err != nil {
		return nil, invalid("no mail provider is connected")
	}
	raw, err := s.box.Open(ct, nonce)
	if err != nil {
		return nil, fmt.Errorf("mailhost: unsealing the credential: %w", err)
	}
	var cfg MigaduConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("mailhost: the stored credential will not parse: %w", err)
	}
	return s.newProvider(cfg), nil
}

// EnableDomain registers a domain with the provider and writes the records it
// requires.
//
// It REFUSES a domain the panel cannot manage records for. That is not
// gatekeeping: on such a domain this feature can do nothing but print
// instructions, and the screen's premise is "domains you've already verified".
func (s *Service) EnableDomain(ctx context.Context, domainName string) (domain.MailDomain, []Record, error) {
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	if domainName == "" {
		return domain.MailDomain{}, nil, invalid("name the domain")
	}
	if s.dns != nil {
		ok, err := s.dns.Verified(ctx, domainName)
		if err == nil && !ok {
			return domain.MailDomain{}, nil, invalid(
				"this panel cannot write DNS for " + domainName + " — connect the zone's provider first, or the records would have to be added by hand")
		}
	}
	p, err := s.provider(ctx)
	if err != nil {
		return domain.MailDomain{}, nil, err
	}
	records, err := p.EnsureDomain(ctx, domainName)
	if err != nil {
		return domain.MailDomain{}, nil, err
	}
	md, err := s.store.CreateMailDomain(ctx, ids.New(ids.PrefixMailDomain), domainName)
	if err != nil {
		return domain.MailDomain{}, nil, err
	}
	s.writeRecords(ctx, md, domainName, records)
	return md, records, nil
}

// writeRecords pushes the provider's records through the DNS automation. A
// failure is RECORDED rather than returned: the domain is enabled either way,
// and an operator who can see which record did not land can fix that one rather
// than starting over.
func (s *Service) writeRecords(ctx context.Context, md domain.MailDomain, domainName string, records []Record) {
	if s.dns == nil {
		return
	}
	var failures []string
	for _, r := range records {
		if err := s.dns.EnsureRecord(ctx, domainName, r); err != nil {
			failures = append(failures, r.Type+" "+RecordName(r, domainName)+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		_ = s.store.SetMailDomainRecords(ctx, md.ID, nil, strings.Join(failures, "; "))
		return
	}
	now := time.Now()
	_ = s.store.SetMailDomainRecords(ctx, md.ID, &now, "")
}

// Records re-reads what the provider requires, for a domain already enabled.
func (s *Service) Records(ctx context.Context, id string) (domain.MailDomain, []Record, error) {
	md, err := s.store.GetMailDomain(ctx, id)
	if err != nil {
		return domain.MailDomain{}, nil, err
	}
	p, err := s.provider(ctx)
	if err != nil {
		return md, nil, err
	}
	records, err := p.RequiredRecords(ctx, md.Domain)
	return md, records, err
}

// Rewrite pushes the records again, for a domain whose last attempt failed.
func (s *Service) Rewrite(ctx context.Context, id string) (domain.MailDomain, error) {
	md, records, err := s.Records(ctx, id)
	if err != nil {
		return md, err
	}
	s.writeRecords(ctx, md, md.Domain, records)
	return s.store.GetMailDomain(ctx, id)
}

func (s *Service) Domains(ctx context.Context) ([]domain.MailDomain, error) {
	return s.store.ListMailDomains(ctx)
}

// DisableDomain forgets the panel's row. The domain and its mailboxes stay at
// the provider: deleting somebody's mail because they turned a panel switch off
// is not a trade this feature gets to make.
func (s *Service) DisableDomain(ctx context.Context, id string) error {
	return s.store.DeleteMailDomain(ctx, id)
}

// MailboxView is one mailbox as the panel shows it: the provider's row, plus
// the panel's own link.
type MailboxView struct {
	Mailbox
	UserID string `json:"user_id"`
}

func (s *Service) Mailboxes(ctx context.Context, domainID string) ([]MailboxView, error) {
	md, err := s.store.GetMailDomain(ctx, domainID)
	if err != nil {
		return nil, err
	}
	p, err := s.provider(ctx)
	if err != nil {
		return nil, err
	}
	// The PROVIDER is the source of truth. The links are the panel's own facts
	// laid over it, and a link with no mailbox is simply not shown.
	boxes, err := p.ListMailboxes(ctx, md.Domain)
	if err != nil {
		return nil, err
	}
	links, err := s.store.ListMailboxLinks(ctx, domainID)
	if err != nil {
		return nil, err
	}
	byAddress := make(map[string]string, len(links))
	for _, l := range links {
		byAddress[l.Address] = l.UserID
	}
	out := make([]MailboxView, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, MailboxView{Mailbox: b, UserID: byAddress[b.Address]})
	}
	return out, nil
}

// CreateMailbox makes one at the provider and links it here.
//
// The password is returned EXACTLY ONCE and is never recoverable — the panel
// cannot read it back because it does not have it: the provider stores the hash
// and the panel forwarded it once. Same contract as an invitation link, applied
// to a credential with the same shape.
func (s *Service) CreateMailbox(ctx context.Context, domainID, local, name, userID string) (MailboxView, string, error) {
	local = strings.ToLower(strings.TrimSpace(local))
	if !ValidLocalPart(local) {
		return MailboxView{}, "", invalid("the part before the @ is lowercase letters, digits, and . - _ +")
	}
	md, err := s.store.GetMailDomain(ctx, domainID)
	if err != nil {
		return MailboxView{}, "", err
	}
	p, err := s.provider(ctx)
	if err != nil {
		return MailboxView{}, "", err
	}
	password, err := generatePassword()
	if err != nil {
		return MailboxView{}, "", err
	}
	box, err := p.CreateMailbox(ctx, md.Domain, local, name, password)
	if err != nil {
		return MailboxView{}, "", err
	}
	// The mailbox now EXISTS at the provider, and the link is the panel's own
	// note about it. A failure here is therefore not a failure of the operation
	// the operator asked for: reporting one would send them to create the
	// mailbox again, and the second attempt would collide with the first.
	//
	// So the link failure is recorded and the success is reported, with the
	// link's absence visible in the answer — UserID is empty, which is exactly
	// what the list will show until somebody re-links it.
	linked := userID
	if _, linkErr := s.store.CreateMailboxLink(ctx, domain.MailboxLink{
		ID: ids.New(ids.PrefixMailboxLink), DomainID: domainID,
		Address: box.Address, UserID: userID,
	}); linkErr != nil {
		s.linkFailed(box.Address, linkErr)
		linked = ""
	}
	return MailboxView{Mailbox: box, UserID: linked}, password, nil
}

func (s *Service) DeleteMailbox(ctx context.Context, domainID, address string) error {
	md, err := s.store.GetMailDomain(ctx, domainID)
	if err != nil {
		return err
	}
	local, dom, err := SplitAddress(address)
	if err != nil {
		return err
	}
	if dom != md.Domain {
		return invalid("that address is not on this domain")
	}
	p, err := s.provider(ctx)
	if err != nil {
		return err
	}
	if err := p.DeleteMailbox(ctx, md.Domain, local); err != nil {
		return err
	}
	return s.store.DeleteMailboxLink(ctx, address)
}

// ResetPassword mints a new one and shows it once.
func (s *Service) ResetPassword(ctx context.Context, domainID, address string) (string, error) {
	md, err := s.store.GetMailDomain(ctx, domainID)
	if err != nil {
		return "", err
	}
	local, dom, err := SplitAddress(address)
	if err != nil {
		return "", err
	}
	if dom != md.Domain {
		return "", invalid("that address is not on this domain")
	}
	p, err := s.provider(ctx)
	if err != nil {
		return "", err
	}
	password, err := generatePassword()
	if err != nil {
		return "", err
	}
	if err := p.SetPassword(ctx, md.Domain, local, password); err != nil {
		return "", err
	}
	return password, nil
}

// generatePassword mints a mailbox password the panel never stores. Long and
// random rather than memorable: it goes into a mail client once and lives in a
// password manager afterwards, so length costs nothing and guessability costs
// somebody their mail.
func generatePassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mailhost: generating a password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
