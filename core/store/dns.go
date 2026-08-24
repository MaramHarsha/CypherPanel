package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// DNS automation persistence (docs/features/dns-automation.md). Domain types
// out; pgx/pgtype stays inside this package.

// ─── Provider ───────────────────────────────────────────────────────────────

func (s *Store) GetDNSProvider(ctx context.Context) (string, []byte, []byte, time.Time, error) {
	row, err := s.q.GetDNSProvider(ctx)
	if err != nil {
		return "", nil, nil, time.Time{}, wrap("getting dns provider", err)
	}
	return row.Kind, row.ConfigCt, row.ConfigNonce, row.UpdatedAt.Time, nil
}

func (s *Store) SetDNSProvider(ctx context.Context, kind string, ct, nonce []byte) error {
	if err := s.q.SetDNSProvider(ctx, db.SetDNSProviderParams{Kind: kind, ConfigCt: ct, ConfigNonce: nonce}); err != nil {
		return wrap("saving dns provider", err)
	}
	return nil
}

func (s *Store) DeleteDNSProvider(ctx context.Context) error {
	if err := s.q.DeleteDNSProvider(ctx); err != nil {
		return wrap("deleting dns provider", err)
	}
	return nil
}

// ─── Zones ──────────────────────────────────────────────────────────────────

func (s *Store) ListDNSZones(ctx context.Context) ([]domain.DNSZone, error) {
	rows, err := s.q.ListDNSZones(ctx)
	if err != nil {
		return nil, wrap("listing dns zones", err)
	}
	out := make([]domain.DNSZone, 0, len(rows))
	for _, r := range rows {
		out = append(out, dnsZoneFromRow(r))
	}
	return out, nil
}

func (s *Store) UpsertDNSZone(ctx context.Context, z domain.DNSZone) (domain.DNSZone, error) {
	row, err := s.q.UpsertDNSZone(ctx, db.UpsertDNSZoneParams{
		ID: z.ID, ProviderZoneID: z.ProviderZoneID, Name: z.Name, Status: z.Status,
	})
	if err != nil {
		return domain.DNSZone{}, wrapUpdate("upserting dns zone", err)
	}
	return dnsZoneFromRow(row), nil
}

// DeleteDNSZonesNotIn prunes the cache to what the provider just listed. A nil
// or empty list clears it, which is what disconnecting does.
//
// A zone that still holds managed records is protected by dns_records'
// ON DELETE RESTRICT, so this returns a foreign-key error rather than silently
// orphaning them — loud is the safe direction here.
func (s *Store) DeleteDNSZonesNotIn(ctx context.Context, names []string) error {
	if names == nil {
		names = []string{}
	}
	if err := s.q.DeleteDNSZonesNotIn(ctx, names); err != nil {
		return wrapDelete("pruning dns zones", err)
	}
	return nil
}

func dnsZoneFromRow(r db.DnsZone) domain.DNSZone {
	return domain.DNSZone{
		ID: r.ID, ProviderZoneID: r.ProviderZoneID, Name: r.Name,
		Status: r.Status, RefreshedAt: r.RefreshedAt.Time,
	}
}

// ─── Records ────────────────────────────────────────────────────────────────

func (s *Store) GetDNSRecordByApplication(ctx context.Context, appID string) (domain.DNSRecord, error) {
	row, err := s.q.GetDNSRecordByApplication(ctx, pgtype.Text{String: appID, Valid: true})
	if err != nil {
		return domain.DNSRecord{}, wrap("getting dns record", err)
	}
	return dnsRecordFromRow(row), nil
}

func (s *Store) UpsertDNSRecord(ctx context.Context, r domain.DNSRecord) (domain.DNSRecord, error) {
	var app pgtype.Text
	if r.ApplicationID != nil {
		app = pgtype.Text{String: *r.ApplicationID, Valid: true}
	}
	row, err := s.q.UpsertDNSRecord(ctx, db.UpsertDNSRecordParams{
		ID: r.ID, ApplicationID: app, ZoneID: r.ZoneID,
		Name: r.Name, Type: r.Type, Content: r.Content,
	})
	if err != nil {
		return domain.DNSRecord{}, wrapUpdate("upserting dns record", err)
	}
	return dnsRecordFromRow(row), nil
}

func (s *Store) TombstoneDNSRecordsForApplication(ctx context.Context, appID string) error {
	if err := s.q.TombstoneDNSRecordsForApplication(ctx, pgtype.Text{String: appID, Valid: true}); err != nil {
		return wrap("tombstoning dns records for application", err)
	}
	return nil
}

func (s *Store) TombstoneDNSRecordsForProject(ctx context.Context, projectID string) error {
	if err := s.q.TombstoneDNSRecordsForProject(ctx, projectID); err != nil {
		return wrap("tombstoning dns records for project", err)
	}
	return nil
}

func (s *Store) TombstoneDNSRecordsForEnvironment(ctx context.Context, envID string) error {
	if err := s.q.TombstoneDNSRecordsForEnvironment(ctx, envID); err != nil {
		return wrap("tombstoning dns records for environment", err)
	}
	return nil
}

// TombstoneOrphanedDNSRecords marks every record whose application is gone.
// Because application_id is ON DELETE SET NULL and environments and projects
// cascade to applications, this single statement covers application, environment
// and project deletion alike (§4.3).
// ListApplicationsWantingDNS returns every application whose route asks for a
// record and whose server can provide the content for one.
func (s *Store) ListApplicationsWantingDNS(ctx context.Context) ([]domain.DNSWant, error) {
	rows, err := s.q.ListApplicationsWantingDNS(ctx)
	if err != nil {
		return nil, wrap("listing applications wanting dns", err)
	}
	out := make([]domain.DNSWant, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.DNSWant{
			ApplicationID: r.ID, Domain: r.RouteDomain, ServerPublicAddress: r.PublicAddress,
		})
	}
	return out, nil
}

func (s *Store) TombstoneOrphanedDNSRecords(ctx context.Context) error {
	if err := s.q.TombstoneOrphanedDNSRecords(ctx); err != nil {
		return wrap("tombstoning orphaned dns records", err)
	}
	return nil
}

func (s *Store) ListDueDNSRecords(ctx context.Context, now time.Time, limit int32) ([]domain.DNSRecordWithZone, error) {
	rows, err := s.q.ListDueDNSRecords(ctx, db.ListDueDNSRecordsParams{
		NextAttemptAt: pgtype.Timestamptz{Time: now, Valid: true},
		Limit:         limit,
	})
	if err != nil {
		return nil, wrap("listing due dns records", err)
	}
	out := make([]domain.DNSRecordWithZone, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.DNSRecordWithZone{
			DNSRecord: domain.DNSRecord{
				ID: r.ID, ApplicationID: ptrText(r.ApplicationID), ZoneID: r.ZoneID,
				Name: r.Name, Type: r.Type, Content: r.Content, Desired: r.Desired,
				ProviderRecordID: ptrText(r.ProviderRecordID), LastError: r.LastError,
				Attempt: int(r.Attempt), NextAttemptAt: ptrTime(r.NextAttemptAt),
				CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
			},
			ProviderZoneID: r.ProviderZoneID,
			ZoneName:       r.ZoneName,
		})
	}
	return out, nil
}

func (s *Store) MarkDNSRecordCreated(ctx context.Context, id, providerRecordID string) (domain.DNSRecord, error) {
	row, err := s.q.MarkDNSRecordCreated(ctx, db.MarkDNSRecordCreatedParams{
		ID: id, ProviderRecordID: pgtype.Text{String: providerRecordID, Valid: true},
	})
	if err != nil {
		return domain.DNSRecord{}, wrap("marking dns record created", err)
	}
	return dnsRecordFromRow(row), nil
}

func (s *Store) DeleteDNSRecordRow(ctx context.Context, id string) error {
	if err := s.q.DeleteDNSRecordRow(ctx, id); err != nil {
		return wrap("deleting dns record row", err)
	}
	return nil
}

func (s *Store) FailDNSRecord(ctx context.Context, id, lastErr string, attempt int, next *time.Time) error {
	if err := s.q.FailDNSRecord(ctx, db.FailDNSRecordParams{
		ID: id, LastError: lastErr, Attempt: int32(attempt), NextAttemptAt: tsFromPtr(next),
	}); err != nil {
		return wrap("recording dns record failure", err)
	}
	return nil
}

func (s *Store) ListDNSRecordsForServer(ctx context.Context, serverID string) ([]domain.DNSRecord, error) {
	rows, err := s.q.ListDNSRecordsForServer(ctx, serverID)
	if err != nil {
		return nil, wrap("listing dns records for server", err)
	}
	out := make([]domain.DNSRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, dnsRecordFromRow(r))
	}
	return out, nil
}

// SetServerPublicAddress records where this server's applications' DNS records
// should point. The plane cannot learn it (§3.4), so the operator supplies it.
func (s *Store) SetServerPublicAddress(ctx context.Context, id, address string) (domain.Server, error) {
	row, err := s.q.SetServerPublicAddress(ctx, db.SetServerPublicAddressParams{ID: id, PublicAddress: address})
	if err != nil {
		return domain.Server{}, wrap("setting server public address", err)
	}
	return serverFromRow(row), nil
}

func dnsRecordFromRow(r db.DnsRecord) domain.DNSRecord {
	return domain.DNSRecord{
		ID: r.ID, ApplicationID: ptrText(r.ApplicationID), ZoneID: r.ZoneID,
		Name: r.Name, Type: r.Type, Content: r.Content, Desired: r.Desired,
		ProviderRecordID: ptrText(r.ProviderRecordID), LastError: r.LastError,
		Attempt: int(r.Attempt), NextAttemptAt: ptrTime(r.NextAttemptAt),
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

// ptrText turns a nullable text column into a *string. NULL application_id is
// what makes a tombstone a tombstone (§3.3), so this distinction is load-bearing
// rather than cosmetic.
func ptrText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	v := t.String
	return &v
}
