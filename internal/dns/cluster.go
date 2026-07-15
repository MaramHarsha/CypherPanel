package dns

import (
	"context"
	"log/slog"
)

// Clustered fans out zone/record writes to a primary DNS node and every
// secondary, keeping the cluster in step (dns-management skill: a record edit
// isn't "done" until the secondaries have it). Reads come from the primary.
//
// This is the app-layer sync strategy: each node's PowerDNS API is updated
// directly, so it works without configuring AXFR/native replication between
// nodes (that remains a valid alternative). Primary failures fail the
// operation; secondary failures are logged and left for the next write/resync
// rather than blocking an edit when one replica is briefly down.
type Clustered struct {
	primary     Provider
	secondaries []Provider
}

// NewClustered wraps a primary with zero or more secondaries. With no
// secondaries it behaves exactly like the primary.
func NewClustered(primary Provider, secondaries []Provider) *Clustered {
	return &Clustered{primary: primary, secondaries: secondaries}
}

func (c *Clustered) fanout(name string, fn func(p Provider) error) error {
	if err := fn(c.primary); err != nil {
		return err
	}
	for i, s := range c.secondaries {
		if err := fn(s); err != nil {
			slog.Warn("dns cluster: secondary out of sync", "op", name, "secondary", i, "error", err)
		}
	}
	return nil
}

func (c *Clustered) EnsureZone(ctx context.Context, zone string, ns []string, defaults []Record) error {
	return c.fanout("ensure-zone", func(p Provider) error { return p.EnsureZone(ctx, zone, ns, defaults) })
}

func (c *Clustered) DeleteZone(ctx context.Context, zone string) error {
	return c.fanout("delete-zone", func(p Provider) error { return p.DeleteZone(ctx, zone) })
}

func (c *Clustered) UpsertRecord(ctx context.Context, zone string, r Record) error {
	return c.fanout("upsert", func(p Provider) error { return p.UpsertRecord(ctx, zone, r) })
}

func (c *Clustered) DeleteRecord(ctx context.Context, zone, name, rtype string) error {
	return c.fanout("delete", func(p Provider) error { return p.DeleteRecord(ctx, zone, name, rtype) })
}

// ListRecords reads from the primary (the authoritative source of truth).
func (c *Clustered) ListRecords(ctx context.Context, zone string) ([]Record, error) {
	return c.primary.ListRecords(ctx, zone)
}

// Resync re-pushes a zone's full record set from the primary to every secondary
// — the repair path when a replica was down during earlier edits.
func (c *Clustered) Resync(ctx context.Context, zone string, ns []string) error {
	records, err := c.primary.ListRecords(ctx, zone)
	if err != nil {
		return err
	}
	for i, s := range c.secondaries {
		if err := s.EnsureZone(ctx, zone, ns, nil); err != nil {
			slog.Warn("dns cluster: resync ensure-zone failed", "secondary", i, "error", err)
			continue
		}
		for _, r := range records {
			if err := s.UpsertRecord(ctx, zone, r); err != nil {
				slog.Warn("dns cluster: resync upsert failed", "secondary", i, "record", r.Name, "error", err)
			}
		}
	}
	return nil
}
