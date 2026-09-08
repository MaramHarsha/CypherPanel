// Package mailhost is email for verified domains, VIA A PROVIDER
// (managed-email.md). Named for what it does — the panel is a mail HOST's
// client, not a mail server — and kept clear of core/mail, which is the panel's
// own outbound SMTP for address confirmations and is a different feature
// entirely.
//
// THE SENTENCE THAT DEFINES THE WHOLE FEATURE: the mail itself lives at a
// provider. CypherPanel creates the DNS, manages mailboxes, and (later) shows
// the inbox — YOUR SERVERS NEVER SEND A BYTE OF MAIL.
//
// That is not a hedge, it is the architecture, and it is worth stating in full
// because "self-hosted panel adds email" reads like Postfix to most people:
//
//	WE DO                                   WE DO NOT
//	Write MX/SPF/DKIM/DMARC through the     Run an MTA, an IMAP server, or a
//	  DNS provider already connected          spam filter
//	Create and delete mailboxes through     Store, queue, relay or deliver a
//	  the provider's API                       message
//	Seal one provider credential            Manage IP reputation, blocklists
//	                                          or PTR records
//
// Running mail is a specialist operational discipline with a reputation system
// attached, and a panel whose first non-negotiable is "one binary + one
// database to install" has no business shipping one.
//
// This package implements PHASES 1 AND 2 — the domain and its records, and
// mailboxes. Webmail is phases 3 and 4 and is deliberately not started: writing
// a webmail client before the mailbox layer exists is guessing at an interface
// to a system nobody has integrated with yet.
package mailhost

import (
	"context"
	"fmt"
	"strings"
)

// Provider is the mail host, consumer-defined with the operations the panel
// actually needs (ENGINEERING rule 6).
//
// The interface exists so a second implementation is a package rather than a
// rewrite — NOT because a second one is planned. One implementation behind an
// interface is honest; three speculative ones are not.
type Provider interface {
	EnsureDomain(ctx context.Context, domainName string) ([]Record, error)
	RequiredRecords(ctx context.Context, domainName string) ([]Record, error)
	ListMailboxes(ctx context.Context, domainName string) ([]Mailbox, error)
	CreateMailbox(ctx context.Context, domainName, local, name, password string) (Mailbox, error)
	DeleteMailbox(ctx context.Context, domainName, local string) error
	SetPassword(ctx context.Context, domainName, local, password string) error
	Test(ctx context.Context) error
}

// Record is one DNS record the mail provider requires.
//
// DKIM is the one asymmetry worth noting: the PROVIDER generates the keypair
// and publishes the public half for the panel to write. The panel never holds a
// DKIM private key — one fewer secret in the system, and only possible because
// the mail lives at the provider.
type Record struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority,omitempty"`
	// Purpose is what this record is FOR, in words: an operator looking at four
	// TXT records needs to know which one is SPF without decoding it.
	Purpose string `json:"purpose"`
}

// Mailbox is one account at the provider. The panel does NOT store these as the
// source of truth — the provider owns them, and the panel lists them through
// the API and caches nothing that would go stale.
type Mailbox struct {
	Address      string `json:"address"`
	Name         string `json:"name"`
	QuotaBytes   int64  `json:"quota_bytes"`
	StorageBytes int64  `json:"storage_bytes"`
}

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// AuthError marks a credential the operator must fix. Mapped to 400 rather than
// 500, because it is input, not a fault — the same distinction the DNS provider
// draws.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// SplitAddress separates the local part from the domain.
func SplitAddress(address string) (local, domainName string, err error) {
	local, domainName, found := strings.Cut(strings.ToLower(strings.TrimSpace(address)), "@")
	if !found || local == "" || domainName == "" {
		return "", "", invalid("that is not an email address")
	}
	return local, domainName, nil
}

// ValidLocalPart is deliberately narrower than RFC 5321 allows. The RFC permits
// quoted strings with spaces and almost any byte; a panel that accepted them
// would be generating addresses that half the internet mishandles, and the
// operator would find out which half from a customer.
func ValidLocalPart(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	if s[0] == '.' || s[len(s)-1] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_' || c == '+':
		default:
			return false
		}
	}
	return !strings.Contains(s, "..")
}

// RecordName renders a record's name against its domain, so an operator reading
// the table sees the fully-qualified name rather than a bare `@`.
func RecordName(r Record, domainName string) string {
	switch {
	case r.Name == "" || r.Name == "@":
		return domainName
	case strings.HasSuffix(r.Name, domainName):
		return r.Name
	default:
		return fmt.Sprintf("%s.%s", strings.TrimSuffix(r.Name, "."), domainName)
	}
}
