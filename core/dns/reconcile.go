package dns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Reconciliation bounds. A DNS write either works or is a misconfiguration the
// operator has to fix, so the ladder is short and the ceiling is generous: there
// is no point hammering Cloudflare for a zone that will not exist until someone
// adds it.
const (
	sweepBatch  = 50
	maxAttempts = 8
)

// backoffFor returns the delay before attempt n+1, capped. Doubling from 30s
// reaches the cap at attempt 6, so a persistently broken record is retried
// roughly hourly rather than abandoned — an operator who fixes a zone should
// see it converge without touching the panel.
func backoffFor(n int) time.Duration {
	d := 30 * time.Second << n
	if d > time.Hour || d <= 0 {
		return time.Hour
	}
	return d
}

// ─── Desired state, written by the callers that change a route ──────────────

// SyncApplication makes the DNS Record for one application match its route.
// It is the single entry point every route change goes through, so the rules
// live in one place:
//
//   - no provider configured → nothing to do (spec §4.1)
//   - no domain, or a domain outside every zone → tombstone whatever we had
//   - verified domain, server has a public address → the record is desired
//   - verified domain, server has NO public address → tombstone, and the API
//     reports the reason (spec §6); writing a record with no content would be
//     worse than writing none
//
// It never calls the provider. Everything here is desired state; convergence is
// the sweeper's job (ADR-005).
func (s *Service) SyncApplication(ctx context.Context, app domain.Application, serverPublicAddress string) error {
	v, err := s.Verify(ctx, app.Route.Domain)
	if err != nil {
		return err
	}
	if !v.Enforced {
		return nil
	}
	host := normalizeHost(app.Route.Domain)
	if host == "" || !v.Verified || serverPublicAddress == "" {
		return s.store.TombstoneDNSRecordsForApplication(ctx, app.ID)
	}
	zones, err := s.store.ListDNSZones(ctx)
	if err != nil {
		return fmt.Errorf("dns: listing zones: %w", err)
	}
	zone, ok := MatchZone(host, zones)
	if !ok {
		return s.store.TombstoneDNSRecordsForApplication(ctx, app.ID)
	}

	// A domain that moved must not leave the old name behind. Tombstoning first
	// and upserting second means the old row is marked absent (and reaped by
	// the sweeper) while the new one is created — the UNIQUE (zone,name,type)
	// key keeps the new row distinct.
	if existing, err := s.store.GetDNSRecordByApplication(ctx, app.ID); err == nil {
		if existing.Name != host {
			if err := s.store.TombstoneDNSRecordsForApplication(ctx, app.ID); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("dns: reading existing record: %w", err)
	}

	_, err = s.store.UpsertDNSRecord(ctx, domain.DNSRecord{
		ID:            ids.New(ids.PrefixDNSRecord),
		ApplicationID: &app.ID,
		ZoneID:        zone.ID,
		Name:          host,
		Type:          "A",
		Content:       serverPublicAddress,
	})
	if err != nil {
		return fmt.Errorf("dns: recording desired DNS: %w", err)
	}
	return nil
}

// ForgetApplication marks every record of one application for deletion. Called
// when the application is deleted — BEFORE the row goes, so application_id is
// still readable.
func (s *Service) ForgetApplication(ctx context.Context, appID string) error {
	return s.store.TombstoneDNSRecordsForApplication(ctx, appID)
}

// ─── Convergence ────────────────────────────────────────────────────────────

// RunSweeper converges due records until ctx is cancelled. It owns its ticker's
// lifecycle (ENGINEERING rule 7) and is wired beside the other sweepers.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepDue(ctx, log)
		}
	}
}

// SweepDue converges every record whose observed state does not match `desired`.
func (s *Service) SweepDue(ctx context.Context, log *slog.Logger) {
	_, cfg, _, err := s.load(ctx)
	if errors.Is(err, ErrNotConfigured) {
		// No provider: nothing converges, and tombstones wait. Disconnecting
		// must not delete anything (spec §4.5), so this is a clean no-op.
		return
	}
	if err != nil {
		log.Error("dns: loading provider for sweep", "error", err)
		return
	}
	// Derive the desired set from the applications table, then reap what no
	// longer belongs. Both halves are deliberately state-owned: a creation path
	// nobody hooked (a template install, a preview) would otherwise produce no
	// record at all, and a deletion nobody hooked would leave one behind
	// forever. Neither depends on an event having fired (§4.3).
	if err := s.deriveDesired(ctx, log); err != nil {
		log.Error("dns: deriving desired records", "error", err)
	}
	if err := s.store.TombstoneOrphanedDNSRecords(ctx); err != nil {
		log.Error("dns: reaping orphaned records", "error", err)
	}
	due, err := s.store.ListDueDNSRecords(ctx, s.now(), sweepBatch)
	if err != nil {
		log.Error("dns: listing due records", "error", err)
		return
	}
	cli := s.newCli(cfg.APIToken)
	for _, r := range due {
		if err := s.converge(ctx, cli, r); err != nil {
			s.fail(ctx, r, err, log)
		}
	}
}

// deriveDesired walks every application that asks for a hostname and makes the
// desired record match. It is SyncApplication's logic applied to the whole
// table rather than to one application at a time, so the REST hooks are an
// optimisation for immediacy and never the thing correctness rests on.
//
// A failure on one application is logged and skipped: one unverifiable domain
// must not stop every other record converging.
func (s *Service) deriveDesired(ctx context.Context, log *slog.Logger) error {
	wants, err := s.store.ListApplicationsWantingDNS(ctx)
	if err != nil {
		return err
	}
	for _, w := range wants {
		app := domain.Application{ID: w.ApplicationID}
		app.Route.Domain = w.Domain
		if err := s.SyncApplication(ctx, app, w.ServerPublicAddress); err != nil {
			log.Error("dns: deriving record", "app_id", w.ApplicationID, "domain", w.Domain, "error", err)
		}
	}
	return nil
}

// converge makes one record match its desired state.
func (s *Service) converge(ctx context.Context, cli Client, r domain.DNSRecordWithZone) error {
	if r.Desired == domain.DNSDesiredAbsent {
		if r.ProviderRecordID == nil {
			// Never created; nothing to delete. Drop the tombstone.
			return s.store.DeleteDNSRecordRow(ctx, r.ID)
		}
		if err := cli.DeleteRecord(ctx, r.ProviderZoneID, *r.ProviderRecordID); err != nil {
			return err
		}
		// Only now is the tombstone spent. This ordering is the whole point of
		// §3.3: the row outlives the application precisely so this delete is
		// guaranteed to happen, and the row is dropped only once it has.
		return s.store.DeleteDNSRecordRow(ctx, r.ID)
	}

	// Desired present. Adopt an existing record only if it is already ours;
	// anything else is the operator's and we do not touch it (spec §4.4).
	existing, found, err := cli.FindRecord(ctx, r.ProviderZoneID, r.Name, r.Type)
	if err != nil {
		return err
	}
	if found {
		if existing.Content == r.Content {
			_, err = s.store.MarkDNSRecordCreated(ctx, r.ID, existing.ProviderID)
			return err
		}
		// A record we previously created and whose content we now want changed
		// is an update; a record we never created is a conflict.
		if r.ProviderRecordID != nil && *r.ProviderRecordID == existing.ProviderID {
			updated, err := cli.UpdateRecord(ctx, r.ProviderZoneID, existing.ProviderID, wantedRecord(r))
			if err != nil {
				return err
			}
			_, err = s.store.MarkDNSRecordCreated(ctx, r.ID, updated.ProviderID)
			return err
		}
		return &ConflictError{Name: r.Name, Existing: existing.Content}
	}

	created, err := cli.CreateRecord(ctx, r.ProviderZoneID, wantedRecord(r))
	if err != nil {
		return err
	}
	_, err = s.store.MarkDNSRecordCreated(ctx, r.ID, created.ProviderID)
	return err
}

// fail records the provider's own words and schedules a retry. The message is
// surfaced per application (spec §6) rather than swallowed, because a DNS
// failure the operator cannot see is a domain that silently never works.
func (s *Service) fail(ctx context.Context, r domain.DNSRecordWithZone, cause error, log *slog.Logger) {
	n := r.Attempt + 1
	var next *time.Time
	if n < maxAttempts {
		t := s.now().Add(backoffFor(r.Attempt))
		next = &t
	}
	log.Error("dns: converging record", "record_id", r.ID, "name", r.Name, "attempt", n, "error", cause)
	if err := s.store.FailDNSRecord(ctx, r.ID, cause.Error(), n, next); err != nil {
		log.Error("dns: recording failure", "record_id", r.ID, "error", err)
	}
}

// wantedRecord is the provider-shaped record one row is asking for. Proxied is
// always false: DNS-only is the v1 decision, and it is what keeps
// routing-and-tls.md's Let's Encrypt HTTP-01 story working unchanged (spec §1).
func wantedRecord(r domain.DNSRecordWithZone) Record {
	return Record{Type: r.Type, Name: r.Name, Content: r.Content, TTL: recordTTL, Proxied: false}
}
