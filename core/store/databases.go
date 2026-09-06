package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// CreateDatabaseWithRevision inserts a database and its first revision inside a
// single transaction.
func (s *Store) CreateDatabaseWithRevision(ctx context.Context, d domain.Database, rev domain.DatabaseRevision) (domain.Database, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Database{}, fmt.Errorf("store: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)

	_, err = qtx.CreateDatabase(ctx, db.CreateDatabaseParams{
		ID:                d.ID,
		EnvironmentID:     d.EnvironmentID,
		Name:              d.Name,
		Engine:            string(d.Engine),
		Version:           d.Version,
		ServerID:          d.ServerID,
		CpuLimit:          float4FromPtr(d.CPULimit),
		MemoryLimitMb:     int4FromPtr(d.MemoryLimitMB),
		VolumeName:        d.VolumeName,
		DataPath:          d.DataPath,
		ExposePort:        int4FromPtr(d.ExposePort),
		Network:           d.Network,
		RootUser:          d.RootUser,
		RootPasswordCt:    d.RootPasswordCT,
		RootPasswordNonce: d.RootPasswordNonce,
		RequirePassword:   d.RequirePassword,
		InitialDatabase:   d.InitialDatabase,
		Status:            d.Status,
		StatusDetail:      d.StatusDetail,
	})
	if err != nil {
		return domain.Database{}, wrapCreate("creating database", err)
	}

	_, err = qtx.CreateDatabaseRevision(ctx, db.CreateDatabaseRevisionParams{
		ID:             rev.ID,
		DatabaseID:     rev.DatabaseID,
		ConfigSnapshot: rev.ConfigSnapshot,
	})
	if err != nil {
		return domain.Database{}, wrapCreate("creating initial database revision", err)
	}

	// Update the database to point to the desired revision.
	row, err := qtx.SetDatabaseDesiredRevision(ctx, db.SetDatabaseDesiredRevisionParams{
		ID:                d.ID,
		DesiredRevisionID: textFromPtr(&rev.ID),
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("setting database desired revision", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Database{}, fmt.Errorf("store: committing tx: %w", err)
	}

	return databaseFromRow(row), nil
}

func (s *Store) GetDatabase(ctx context.Context, id string) (domain.Database, error) {
	row, err := s.q.GetDatabase(ctx, id)
	if err != nil {
		return domain.Database{}, wrap("getting database", err)
	}
	return databaseFromRow(row), nil
}

func (s *Store) ListDatabasesByEnvironment(ctx context.Context, envID string) ([]domain.Database, error) {
	rows, err := s.q.ListDatabasesByEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("store: listing databases by environment: %w", err)
	}
	out := make([]domain.Database, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseFromRow(r))
	}
	return out, nil
}

// ListDatabaseConfigsByEnvironment returns databases without their sealed root
// password, for the reason ListApplicationConfigsByEnvironment exists.
func (s *Store) ListDatabaseConfigsByEnvironment(ctx context.Context, envID string) ([]domain.DatabaseConfig, error) {
	dbs, err := s.ListDatabasesByEnvironment(ctx, envID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.DatabaseConfig, 0, len(dbs))
	for _, d := range dbs {
		out = append(out, d.ConfigView())
	}
	return out, nil
}

func (s *Store) ListDatabasesByServer(ctx context.Context, serverID string) ([]domain.Database, error) {
	rows, err := s.q.ListDatabasesByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("store: listing databases by server: %w", err)
	}
	out := make([]domain.Database, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseFromRow(r))
	}
	return out, nil
}

func (s *Store) ListPendingDeleteDatabases(ctx context.Context) ([]domain.Database, error) {
	rows, err := s.q.ListPendingDeleteDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listing pending delete databases: %w", err)
	}
	out := make([]domain.Database, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateDatabaseConfig(ctx context.Context, d domain.Database) (domain.Database, error) {
	row, err := s.q.UpdateDatabaseConfig(ctx, db.UpdateDatabaseConfigParams{
		ID:            d.ID,
		Name:          d.Name,
		Version:       d.Version,
		CpuLimit:      float4FromPtr(d.CPULimit),
		MemoryLimitMb: int4FromPtr(d.MemoryLimitMB),
		ExposePort:    int4FromPtr(d.ExposePort),
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("updating database config", err)
	}
	return databaseFromRow(row), nil
}

func (s *Store) UpdateDatabasePassword(ctx context.Context, id string, ct, nonce []byte) error {
	err := s.q.UpdateDatabasePassword(ctx, db.UpdateDatabasePasswordParams{
		ID:                id,
		RootPasswordCt:    ct,
		RootPasswordNonce: nonce,
	})
	if err != nil {
		return wrapUpdate("updating database password", err)
	}
	return nil
}

func (s *Store) SetDatabaseDesiredRevision(ctx context.Context, id string, revID string) (domain.Database, error) {
	row, err := s.q.SetDatabaseDesiredRevision(ctx, db.SetDatabaseDesiredRevisionParams{
		ID:                id,
		DesiredRevisionID: textFromPtr(&revID),
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("setting database desired revision", err)
	}
	return databaseFromRow(row), nil
}

// SetDatabaseDesiredState records the operator's run/stop intent (authoritative
// for the scheduler; distinct from the observed status).
func (s *Store) SetDatabaseDesiredState(ctx context.Context, id, desiredState string) (domain.Database, error) {
	row, err := s.q.SetDatabaseDesiredState(ctx, db.SetDatabaseDesiredStateParams{
		ID:           id,
		DesiredState: desiredState,
	})
	if err != nil {
		return domain.Database{}, wrapUpdate("setting database desired state", err)
	}
	return databaseFromRow(row), nil
}

func (s *Store) SetDatabaseStatus(ctx context.Context, id, status, detail string) error {
	err := s.q.SetDatabaseStatus(ctx, db.SetDatabaseStatusParams{
		ID:           id,
		Status:       status,
		StatusDetail: detail,
	})
	if err != nil {
		return wrapUpdate("setting database status", err)
	}
	return nil
}

func (s *Store) SetDatabaseObservedStatus(ctx context.Context, id, status, detail, observedRevisionID string, observedAt time.Time) error {
	err := s.q.SetDatabaseObservedStatus(ctx, db.SetDatabaseObservedStatusParams{
		ID:                 id,
		Status:             status,
		StatusDetail:       detail,
		ObservedRevisionID: observedRevisionID,
		StatusObservedAt:   tsFromTime(observedAt),
	})
	if err != nil {
		return wrapUpdate("setting database observed status", err)
	}
	return nil
}

func (s *Store) SetDatabasePendingDelete(ctx context.Context, id string, deleteVolume bool) error {
	err := s.q.SetDatabasePendingDelete(ctx, db.SetDatabasePendingDeleteParams{
		ID:           id,
		DeleteVolume: deleteVolume,
	})
	if err != nil {
		return wrapUpdate("setting database pending delete", err)
	}
	return nil
}

func (s *Store) DeleteDatabase(ctx context.Context, id string) error {
	if err := s.q.DeleteDatabase(ctx, id); err != nil {
		return wrapDelete("deleting database", err)
	}
	return nil
}

func (s *Store) CreateDatabaseRevision(ctx context.Context, rev domain.DatabaseRevision) (domain.DatabaseRevision, error) {
	row, err := s.q.CreateDatabaseRevision(ctx, db.CreateDatabaseRevisionParams{
		ID:             rev.ID,
		DatabaseID:     rev.DatabaseID,
		ConfigSnapshot: rev.ConfigSnapshot,
	})
	if err != nil {
		return domain.DatabaseRevision{}, wrapCreate("creating database revision", err)
	}
	return databaseRevisionFromRow(row), nil
}

func (s *Store) GetDatabaseRevision(ctx context.Context, id string) (domain.DatabaseRevision, error) {
	row, err := s.q.GetDatabaseRevision(ctx, id)
	if err != nil {
		return domain.DatabaseRevision{}, wrap("getting database revision", err)
	}
	return databaseRevisionFromRow(row), nil
}

func (s *Store) GetLatestDatabaseRevision(ctx context.Context, dbID string) (domain.DatabaseRevision, error) {
	row, err := s.q.GetLatestDatabaseRevision(ctx, dbID)
	if err != nil {
		return domain.DatabaseRevision{}, wrap("getting latest database revision", err)
	}
	return databaseRevisionFromRow(row), nil
}

func (s *Store) ListDatabaseRevisions(ctx context.Context, dbID string) ([]domain.DatabaseRevision, error) {
	rows, err := s.q.ListDatabaseRevisions(ctx, dbID)
	if err != nil {
		return nil, fmt.Errorf("store: listing database revisions: %w", err)
	}
	out := make([]domain.DatabaseRevision, 0, len(rows))
	for _, r := range rows {
		out = append(out, databaseRevisionFromRow(r))
	}
	return out, nil
}

func databaseFromRow(r db.Database) domain.Database {
	return domain.Database{
		ID:                 r.ID,
		EnvironmentID:      r.EnvironmentID,
		Name:               r.Name,
		Engine:             domain.DbEngine(r.Engine),
		Version:            r.Version,
		ServerID:           r.ServerID,
		CPULimit:           ptrFromFloat4(r.CpuLimit),
		MemoryLimitMB:      ptrFromInt4(r.MemoryLimitMb),
		VolumeName:         r.VolumeName,
		DataPath:           r.DataPath,
		ExposePort:         ptrFromInt4(r.ExposePort),
		Network:            r.Network,
		RootUser:           r.RootUser,
		RootPasswordCT:     r.RootPasswordCt,
		RootPasswordNonce:  r.RootPasswordNonce,
		RequirePassword:    r.RequirePassword,
		InitialDatabase:    r.InitialDatabase,
		DesiredRevisionID:  ptrFromText(r.DesiredRevisionID),
		DesiredState:       r.DesiredState,
		Status:             r.Status,
		StatusDetail:       r.StatusDetail,
		ObservedRevisionID: r.ObservedRevisionID,
		StatusObservedAt:   ptrTime(r.StatusObservedAt),
		PendingDelete:      r.PendingDelete,
		DeleteVolume:       r.DeleteVolume,
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}

func databaseRevisionFromRow(r db.DatabaseRevision) domain.DatabaseRevision {
	return domain.DatabaseRevision{
		ID:             r.ID,
		DatabaseID:     r.DatabaseID,
		ConfigSnapshot: r.ConfigSnapshot,
		CreatedAt:      r.CreatedAt.Time,
	}
}
func float4FromPtr(f *float64) pgtype.Float4 {
	if f == nil {
		return pgtype.Float4{}
	}
	return pgtype.Float4{Float32: float32(*f), Valid: true}
}

func ptrFromFloat4(f pgtype.Float4) *float64 {
	if !f.Valid {
		return nil
	}
	v := float64(f.Float32)
	return &v
}

func int4FromPtr(i *int) pgtype.Int4 {
	if i == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*i), Valid: true}
}

func ptrFromInt4(i pgtype.Int4) *int {
	if !i.Valid {
		return nil
	}
	v := int(i.Int32)
	return &v
}
