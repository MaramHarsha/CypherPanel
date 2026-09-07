package store

// Mail provider, domains and mailbox links (managed-email.md §§4, 5).

import (
	"context"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

func mailDomainFromRow(r db.MailDomain) domain.MailDomain {
	return domain.MailDomain{
		ID: r.ID, Domain: r.Domain,
		RecordsWrittenAt: ptrTime(r.RecordsWrittenAt), LastError: r.LastError,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func (s *Store) GetMailProvider(ctx context.Context) (kind string, ct, nonce []byte, err error) {
	row, err := s.q.GetMailProvider(ctx)
	if err != nil {
		return "", nil, nil, wrap("reading the mail provider", err)
	}
	return row.Kind, row.ConfigCt, row.ConfigNonce, nil
}

func (s *Store) SetMailProvider(ctx context.Context, kind string, ct, nonce []byte) error {
	if _, err := s.q.SetMailProvider(ctx, db.SetMailProviderParams{
		Kind: kind, ConfigCt: ct, ConfigNonce: nonce,
	}); err != nil {
		return wrap("saving the mail provider", err)
	}
	return nil
}

func (s *Store) DeleteMailProvider(ctx context.Context) error {
	if err := s.q.DeleteMailProvider(ctx); err != nil {
		return wrap("disconnecting the mail provider", err)
	}
	return nil
}

func (s *Store) CreateMailDomain(ctx context.Context, id, name string) (domain.MailDomain, error) {
	row, err := s.q.CreateMailDomain(ctx, db.CreateMailDomainParams{ID: id, Domain: name})
	if err != nil {
		return domain.MailDomain{}, wrap("enabling mail on the domain", err)
	}
	return mailDomainFromRow(row), nil
}

func (s *Store) GetMailDomain(ctx context.Context, id string) (domain.MailDomain, error) {
	row, err := s.q.GetMailDomain(ctx, id)
	if err != nil {
		return domain.MailDomain{}, wrap("reading the mail domain", err)
	}
	return mailDomainFromRow(row), nil
}

func (s *Store) GetMailDomainByName(ctx context.Context, name string) (domain.MailDomain, error) {
	row, err := s.q.GetMailDomainByName(ctx, name)
	if err != nil {
		return domain.MailDomain{}, wrap("reading the mail domain", err)
	}
	return mailDomainFromRow(row), nil
}

func (s *Store) ListMailDomains(ctx context.Context) ([]domain.MailDomain, error) {
	rows, err := s.q.ListMailDomains(ctx)
	if err != nil {
		return nil, wrap("listing mail domains", err)
	}
	out := make([]domain.MailDomain, 0, len(rows))
	for _, r := range rows {
		out = append(out, mailDomainFromRow(r))
	}
	return out, nil
}

func (s *Store) SetMailDomainRecords(ctx context.Context, id string, at *time.Time, lastError string) error {
	if err := s.q.SetMailDomainRecords(ctx, db.SetMailDomainRecordsParams{
		ID: id, RecordsWrittenAt: tsFromPtr(at), LastError: lastError,
	}); err != nil {
		return wrap("recording the domain's records", err)
	}
	return nil
}

func (s *Store) DeleteMailDomain(ctx context.Context, id string) error {
	if err := s.q.DeleteMailDomain(ctx, id); err != nil {
		return wrap("disabling mail on the domain", err)
	}
	return nil
}

func (s *Store) CreateMailboxLink(ctx context.Context, l domain.MailboxLink) (domain.MailboxLink, error) {
	row, err := s.q.CreateMailboxLink(ctx, db.CreateMailboxLinkParams{
		ID: l.ID, DomainID: l.DomainID, Address: l.Address, UserID: nullText(l.UserID),
	})
	if err != nil {
		return domain.MailboxLink{}, wrap("linking the mailbox", err)
	}
	return domain.MailboxLink{
		ID: row.ID, DomainID: row.DomainID, Address: row.Address,
		UserID: row.UserID.String, CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (s *Store) ListMailboxLinks(ctx context.Context, domainID string) ([]domain.MailboxLink, error) {
	rows, err := s.q.ListMailboxLinks(ctx, domainID)
	if err != nil {
		return nil, wrap("listing mailbox links", err)
	}
	out := make([]domain.MailboxLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.MailboxLink{
			ID: r.ID, DomainID: r.DomainID, Address: r.Address,
			UserID: r.UserID.String, CreatedAt: r.CreatedAt.Time,
		})
	}
	return out, nil
}

func (s *Store) DeleteMailboxLink(ctx context.Context, address string) error {
	if err := s.q.DeleteMailboxLink(ctx, address); err != nil {
		return wrap("unlinking the mailbox", err)
	}
	return nil
}
