package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PowerDNS talks to the PowerDNS authoritative HTTP API (the MVP default).
type PowerDNS struct {
	apiURL   string
	apiKey   string
	serverID string
	hc       *http.Client
}

// NewPowerDNS builds a client for a PowerDNS API base URL (e.g.
// http://127.0.0.1:8081) and API key.
func NewPowerDNS(apiURL, apiKey string) *PowerDNS {
	return &PowerDNS{
		apiURL:   strings.TrimRight(apiURL, "/"),
		apiKey:   apiKey,
		serverID: "localhost",
		hc:       &http.Client{Timeout: 10 * time.Second},
	}
}

// canonical returns a zone/name with a single trailing dot (PowerDNS canonical).
func canonical(name string) string {
	return strings.TrimSuffix(name, ".") + "."
}

func (p *PowerDNS) base() string {
	return fmt.Sprintf("%s/api/v1/servers/%s", p.apiURL, p.serverID)
}

// do issues a request; out (optional) is decoded from the 2xx body.
func (p *PowerDNS) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base()+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-Key", p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("dns: powerdns request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("dns: powerdns %s %s: %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("dns: decoding powerdns response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// pdnsZone / pdnsRRSet mirror the PowerDNS API shapes we use.
type pdnsRecord struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}
type pdnsRRSet struct {
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	TTL        int          `json:"ttl"`
	ChangeType string       `json:"changetype,omitempty"`
	Records    []pdnsRecord `json:"records"`
}
type pdnsZone struct {
	Name        string      `json:"name"`
	Kind        string      `json:"kind,omitempty"`
	Nameservers []string    `json:"nameservers,omitempty"`
	RRSets      []pdnsRRSet `json:"rrsets,omitempty"`
}

// EnsureZone creates the zone if absent (idempotent), then upserts the default
// records. Nameservers seed the zone's NS/SOA on creation.
func (p *PowerDNS) EnsureZone(ctx context.Context, zone string, nameservers []string, defaults []Record) error {
	z := canonical(zone)
	// Exists?
	status, _ := p.do(ctx, http.MethodGet, "/zones/"+z, nil, nil)
	if status != http.StatusOK {
		ns := make([]string, 0, len(nameservers))
		for _, n := range nameservers {
			ns = append(ns, canonical(n))
		}
		if _, err := p.do(ctx, http.MethodPost, "/zones", pdnsZone{
			Name: z, Kind: "Native", Nameservers: ns,
		}, nil); err != nil {
			return err
		}
	}
	for _, r := range defaults {
		if err := p.UpsertRecord(ctx, zone, r); err != nil {
			return err
		}
	}
	return nil
}

// Zones lists all zone names (canonical, with trailing dot).
func (p *PowerDNS) Zones(ctx context.Context) ([]string, error) {
	var zones []pdnsZone
	if _, err := p.do(ctx, http.MethodGet, "/zones", nil, &zones); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		out = append(out, z.Name)
	}
	return out, nil
}

func (p *PowerDNS) DeleteZone(ctx context.Context, zone string) error {
	status, err := p.do(ctx, http.MethodDelete, "/zones/"+canonical(zone), nil, nil)
	if status == http.StatusNotFound {
		return nil // idempotent
	}
	return err
}

// ListRecords returns the zone's RRsets (excluding the auto-managed SOA).
func (p *PowerDNS) ListRecords(ctx context.Context, zone string) ([]Record, error) {
	var z pdnsZone
	if _, err := p.do(ctx, http.MethodGet, "/zones/"+canonical(zone), nil, &z); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(z.RRSets))
	for _, rr := range z.RRSets {
		if rr.Type == "SOA" {
			continue
		}
		contents := make([]string, 0, len(rr.Records))
		for _, rec := range rr.Records {
			contents = append(contents, rec.Content)
		}
		out = append(out, Record{Name: rr.Name, Type: rr.Type, TTL: rr.TTL, Contents: contents})
	}
	return out, nil
}

// UpsertRecord replaces the RRset at (name, type) with the given contents.
func (p *PowerDNS) UpsertRecord(ctx context.Context, zone string, r Record) error {
	ttl := r.TTL
	if ttl == 0 {
		ttl = 3600
	}
	recs := make([]pdnsRecord, 0, len(r.Contents))
	for _, c := range r.Contents {
		recs = append(recs, pdnsRecord{Content: formatContent(r.Type, strings.TrimSpace(c))})
	}
	patch := pdnsZone{RRSets: []pdnsRRSet{{
		Name: canonical(r.Name), Type: r.Type, TTL: ttl, ChangeType: "REPLACE", Records: recs,
	}}}
	_, err := p.do(ctx, http.MethodPatch, "/zones/"+canonical(zone), patch, nil)
	return err
}

// formatContent canonicalises record data to what PowerDNS expects: TXT values
// are quoted, and hostname targets (CNAME/NS/MX/SRV) get a trailing dot.
func formatContent(rtype, c string) string {
	switch rtype {
	case "TXT":
		if strings.HasPrefix(c, "\"") {
			return c
		}
		return "\"" + strings.ReplaceAll(c, "\"", "\\\"") + "\""
	case "CNAME", "NS":
		return canonical(c)
	case "MX":
		// "<priority> <host>" → dot the host
		if f := strings.Fields(c); len(f) == 2 {
			return f[0] + " " + canonical(f[1])
		}
	case "SRV":
		// "<priority> <weight> <port> <target>" → dot the target
		if f := strings.Fields(c); len(f) == 4 {
			return f[0] + " " + f[1] + " " + f[2] + " " + canonical(f[3])
		}
	}
	return c
}

// DeleteRecord removes the RRset at (name, type).
func (p *PowerDNS) DeleteRecord(ctx context.Context, zone, name, rtype string) error {
	patch := pdnsZone{RRSets: []pdnsRRSet{{
		Name: canonical(name), Type: rtype, ChangeType: "DELETE",
	}}}
	_, err := p.do(ctx, http.MethodPatch, "/zones/"+canonical(zone), patch, nil)
	return err
}
