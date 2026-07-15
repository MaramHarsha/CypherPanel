// Package dns manages authoritative DNS for hosted domains. PowerDNS is the MVP
// default behind the Provider interface (chosen because it is REST-API driven,
// not zone-file editing); a BIND adapter can drop in later. Per-record-type
// validation happens here, at the boundary, so PowerDNS never rejects a bad
// record opaquely (see the dns-management skill).
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// ErrUnsupported is returned when no DNS backend is configured.
var ErrUnsupported = errors.New("dns: no DNS backend configured")

// SupportedTypes are the record types the zone editor exposes.
var SupportedTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"}

// Record is one RRset: a name+type with one or more data lines and a TTL.
type Record struct {
	Name     string   `json:"name"`     // FQDN (trailing dot normalised by the provider)
	Type     string   `json:"type"`     // A, AAAA, CNAME, MX, TXT, SRV, CAA, NS
	TTL      int      `json:"ttl"`      // seconds
	Contents []string `json:"contents"` // record data lines (e.g. "10 mail.example.com.")
}

// Provider is the DNS backend. Implementations manage zones and RRsets and must
// be idempotent (upsert/delete converge).
type Provider interface {
	EnsureZone(ctx context.Context, zone string, nameservers []string, defaults []Record) error
	DeleteZone(ctx context.Context, zone string) error
	ListRecords(ctx context.Context, zone string) ([]Record, error)
	UpsertRecord(ctx context.Context, zone string, r Record) error
	DeleteRecord(ctx context.Context, zone, name, rtype string) error
}

var hostnameRe = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9_]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9_])?\.)+[a-zA-Z]{2,}\.?$`)

// isType reports whether t is a supported record type.
func isType(t string) bool {
	for _, s := range SupportedTypes {
		if s == t {
			return true
		}
	}
	return false
}

// ValidateRecord checks a record per its type and rejects malformed data with a
// clear message. It does NOT enforce cross-record rules (CNAME coexistence) —
// that needs the zone's other records; see ValidateAgainstZone.
func ValidateRecord(r Record) error {
	if !isType(r.Type) {
		return fmt.Errorf("unsupported record type %q", r.Type)
	}
	if r.Name == "" {
		return errors.New("name is required")
	}
	if r.TTL < 0 || r.TTL > 604800 {
		return errors.New("ttl must be between 0 and 604800")
	}
	if len(r.Contents) == 0 {
		return errors.New("at least one record value is required")
	}
	for _, c := range r.Contents {
		if err := validateContent(r.Type, strings.TrimSpace(c)); err != nil {
			return err
		}
	}
	// CNAME is single-valued at a name.
	if r.Type == "CNAME" && len(r.Contents) != 1 {
		return errors.New("CNAME must have exactly one value")
	}
	return nil
}

func validateContent(rtype, c string) error {
	switch rtype {
	case "A":
		if ip := net.ParseIP(c); ip == nil || ip.To4() == nil {
			return fmt.Errorf("%q is not a valid IPv4 address", c)
		}
	case "AAAA":
		if ip := net.ParseIP(c); ip == nil || ip.To4() != nil {
			return fmt.Errorf("%q is not a valid IPv6 address", c)
		}
	case "CNAME", "NS":
		if !hostnameRe.MatchString(c) {
			return fmt.Errorf("%q is not a valid hostname", c)
		}
	case "MX":
		// "<priority> <hostname>"
		fields := strings.Fields(c)
		if len(fields) != 2 {
			return errors.New("MX must be '<priority> <hostname>'")
		}
		if _, err := strconv.Atoi(fields[0]); err != nil {
			return errors.New("MX priority must be a number")
		}
		if !hostnameRe.MatchString(fields[1]) {
			return fmt.Errorf("MX target %q is not a valid hostname", fields[1])
		}
	case "SRV":
		// "<priority> <weight> <port> <target>"
		fields := strings.Fields(c)
		if len(fields) != 4 {
			return errors.New("SRV must be '<priority> <weight> <port> <target>'")
		}
		for _, n := range fields[:3] {
			if _, err := strconv.Atoi(n); err != nil {
				return errors.New("SRV priority, weight and port must be numbers")
			}
		}
		if !hostnameRe.MatchString(fields[3]) {
			return fmt.Errorf("SRV target %q is not a valid hostname", fields[3])
		}
	case "CAA":
		// "<flag> <tag> <value>"
		fields := strings.SplitN(c, " ", 3)
		if len(fields) != 3 {
			return errors.New("CAA must be '<flag> <tag> \"<value>\"'")
		}
		if _, err := strconv.Atoi(fields[0]); err != nil {
			return errors.New("CAA flag must be a number")
		}
		switch fields[1] {
		case "issue", "issuewild", "iodef":
		default:
			return errors.New("CAA tag must be issue, issuewild or iodef")
		}
	case "TXT":
		if len(c) > 65535 {
			return errors.New("TXT value too long")
		}
	}
	return nil
}

// ValidateAgainstZone enforces cross-record rules: a CNAME cannot coexist with
// any other record at the same name, and no record may share a name with an
// existing CNAME (RFC 1034).
func ValidateAgainstZone(r Record, existing []Record) error {
	name := normalizeName(r.Name)
	for _, e := range existing {
		if normalizeName(e.Name) != name || e.Type == r.Type {
			continue
		}
		if r.Type == "CNAME" || e.Type == "CNAME" {
			return fmt.Errorf("a CNAME at %s cannot coexist with other records", r.Name)
		}
	}
	return nil
}

func normalizeName(n string) string {
	return strings.ToLower(strings.TrimSuffix(n, "."))
}

// NameInZone reports whether a record name belongs to zone — it must equal the
// zone apex or be a subdomain of it. Prevents an account editing another
// domain's records via the zone editor.
func NameInZone(name, zone string) bool {
	n, z := normalizeName(name), normalizeName(zone)
	return n == z || strings.HasSuffix(n, "."+z)
}
