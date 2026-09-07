package domain

import "time"

// Email for verified domains, via a provider (managed-email.md).

// MailDomain is a verified domain the operator asked the panel to route mail
// for. The PROVIDER owns the domain's mail configuration; this row is the
// panel's own fact about which of its verified domains that applies to.
type MailDomain struct {
	ID     string
	Domain string
	// RecordsWrittenAt is observed: were the records the provider asked for
	// actually written through the DNS provider? nil means asked but not yet
	// confirmed, which reads as "pending" rather than as "done".
	RecordsWrittenAt *time.Time
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// MailboxLink ties a provider mailbox to a panel account. It is the ONLY thing
// about a mailbox this panel stores: the provider owns the mailbox itself, and
// caching one here would be a second answer to "does this address exist".
type MailboxLink struct {
	ID        string
	DomainID  string
	Address   string
	UserID    string
	CreatedAt time.Time
}
