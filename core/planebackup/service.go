package planebackup

// The nightly run and its retention (plane-disaster-recovery.md §§5, 9).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence the service needs (consumer-defined).
type Store interface {
	GetPlaneDRConfig(ctx context.Context) (domain.PlaneDRConfig, error)
	SetPlaneDRConfig(ctx context.Context, c domain.PlaneDRConfig) (domain.PlaneDRConfig, error)
	DeletePlaneDRConfig(ctx context.Context) error
	SetPlaneDRRun(ctx context.Context, at time.Time, status, detail string) error
	MarkPlaneDRRecipientVerified(ctx context.Context, at time.Time) error
	CreatePlaneSnapshot(ctx context.Context, sn domain.PlaneSnapshot) (domain.PlaneSnapshot, error)
	FinishPlaneSnapshot(ctx context.Context, id string, size int64, sha string, rows int64, status, detail string) error
	ListPlaneSnapshots(ctx context.Context, limit int) ([]domain.PlaneSnapshot, error)
	ListPlaneSnapshotsBeyondRetention(ctx context.Context, keep int) ([]domain.PlaneSnapshot, error)
	MarkPlaneSnapshotPruned(ctx context.Context, id string) error
	GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error)
}

// ObjectStore writes and deletes the archive.
type ObjectStore interface {
	Put(ctx context.Context, target domain.BackupTarget, key string, body []byte) error
	Delete(ctx context.Context, target domain.BackupTarget, key string) error
}

// Options wires the service.
type Options struct {
	Store        Store
	DB           Copier
	Enc          Encryptor
	Objects      ObjectStore
	PanelVersion string
	// MasterKey travels INSIDE the archive. A snapshot without it is worthless
	// — the plane fails closed on a wrong key rather than minting a new CA — so
	// leaving it out would make the archive a database nobody can open.
	MasterKey string
	Log       *slog.Logger
	Now       func() time.Time
}

// Service owns the nightly run.
type Service struct{ o Options }

func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// Config reads the singleton. Its EXISTENCE is the armed answer.
func (s *Service) Config(ctx context.Context) (domain.PlaneDRConfig, bool) {
	c, err := s.o.Store.GetPlaneDRConfig(ctx)
	if err != nil {
		return domain.PlaneDRConfig{}, false
	}
	return c, true
}

// Arm saves the configuration. When the operator asks the panel to generate the
// pair, the private half is returned HERE and nowhere else, ever: this function
// is the only place it exists, and it is not written to the row it configures.
func (s *Service) Arm(ctx context.Context, c domain.PlaneDRConfig, generate bool) (domain.PlaneDRConfig, string, error) {
	identity := ""
	if generate {
		recipient, id, err := GenerateRecoveryKey()
		if err != nil {
			return domain.PlaneDRConfig{}, "", err
		}
		c.Recipient, c.RecipientMode, identity = recipient, domain.RecipientGenerated, id
	} else {
		c.RecipientMode = domain.RecipientProvided
	}
	if !ValidRecipient(c.Recipient) {
		return domain.PlaneDRConfig{}, "", fmt.Errorf("planebackup: that is not an age public key — they start with age1")
	}
	if c.PathPrefix == "" {
		c.PathPrefix = "plane-state"
	}
	if c.RetentionCount < 1 {
		c.RetentionCount = 14
	}
	saved, err := s.o.Store.SetPlaneDRConfig(ctx, c)
	if err != nil {
		return domain.PlaneDRConfig{}, "", err
	}
	return saved, identity, nil
}

// Disarm removes the configuration. Archives already in the bucket are LEFT
// ALONE: deleting an operator's off-site copies from a panel action is the one
// mistake with no undo, and disarming is not the same decision as discarding.
func (s *Service) Disarm(ctx context.Context) error {
	return s.o.Store.DeletePlaneDRConfig(ctx)
}

// RunNow takes one snapshot.
func (s *Service) RunNow(ctx context.Context) (domain.PlaneSnapshot, error) {
	cfg, armed := s.Config(ctx)
	if !armed {
		return domain.PlaneSnapshot{}, fmt.Errorf("planebackup: disaster recovery is not armed")
	}
	target, err := s.o.Store.GetBackupTarget(ctx, cfg.TargetID)
	if err != nil {
		return domain.PlaneSnapshot{}, fmt.Errorf("planebackup: reading the backup target: %w", err)
	}

	now := s.o.Now().UTC()
	key := strings.Trim(cfg.PathPrefix, "/") + "/" + now.Format("2006-01-02T150405Z") + ".tar.age"

	// The row is written BEFORE the work, so a plane that dies mid-snapshot
	// leaves a `running` row an operator can see rather than silence
	// (ENGINEERING rule 15).
	sn, err := s.o.Store.CreatePlaneSnapshot(ctx, domain.PlaneSnapshot{
		ID: ids.New(ids.PrefixPlaneSnapshot), ObjectKey: key,
		PanelVersion: s.o.PanelVersion, Recipient: cfg.Recipient,
	})
	if err != nil {
		return domain.PlaneSnapshot{}, err
	}

	fail := func(err error) (domain.PlaneSnapshot, error) {
		_ = s.o.Store.FinishPlaneSnapshot(ctx, sn.ID, 0, "", 0, "failed", truncate(err.Error(), 500))
		_ = s.o.Store.SetPlaneDRRun(ctx, s.o.Now(), "failed", truncate(err.Error(), 500))
		return domain.PlaneSnapshot{}, err
	}

	var buf bytes.Buffer
	man, err := Export(ctx, s.o.DB, s.o.Enc, &buf, cfg.Recipient, s.o.MasterKey, s.o.PanelVersion)
	if err != nil {
		return fail(err)
	}
	body := buf.Bytes()
	sum := sha256.Sum256(body)

	if err := s.o.Objects.Put(ctx, target, key, body); err != nil {
		return fail(fmt.Errorf("uploading the snapshot: %w", err))
	}

	var rows int64
	for _, t := range man.Tables {
		rows += t.Rows
	}
	if err := s.o.Store.FinishPlaneSnapshot(ctx, sn.ID, int64(len(body)), hex.EncodeToString(sum[:]), rows, "succeeded", ""); err != nil {
		s.o.Log.Error("plane backup: recording the snapshot", "error", err)
	}
	if err := s.o.Store.SetPlaneDRRun(ctx, s.o.Now(), "succeeded", ""); err != nil {
		s.o.Log.Debug("plane backup: recording the run", "error", err)
	}
	s.prune(ctx, cfg, target)

	sn.SizeBytes, sn.RowCount, sn.SHA256, sn.Status = int64(len(body)), rows, hex.EncodeToString(sum[:]), "succeeded"
	sn.SchemaVersion = man.SchemaVersion
	return sn, nil
}

// prune deletes what retention no longer keeps — AFTER a successful upload,
// never before. A failed run must not cost the operator the copy they still
// have.
func (s *Service) prune(ctx context.Context, cfg domain.PlaneDRConfig, target domain.BackupTarget) {
	stale, err := s.o.Store.ListPlaneSnapshotsBeyondRetention(ctx, cfg.RetentionCount)
	if err != nil || len(stale) == 0 {
		return
	}
	for _, sn := range stale {
		if err := s.o.Objects.Delete(ctx, target, sn.ObjectKey); err != nil {
			s.o.Log.Warn("plane backup: pruning an object", "key", sn.ObjectKey, "error", err)
			continue
		}
		if err := s.o.Store.MarkPlaneSnapshotPruned(ctx, sn.ID); err != nil {
			s.o.Log.Warn("plane backup: marking a snapshot pruned", "id", sn.ID, "error", err)
		}
	}
}

// Run is the owned goroutine. It sweeps on a tick and takes a snapshot when the
// schedule is due — the same shape every other scheduled thing in this panel
// uses, so there is one place to reason about "did it run".
func (s *Service) Run(ctx context.Context, every time.Duration, due func(schedule string, last *time.Time, now time.Time) bool) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cfg, armed := s.Config(ctx)
			if !armed || !due(cfg.Schedule, cfg.LastRunAt, s.o.Now()) {
				continue
			}
			if _, err := s.RunNow(ctx); err != nil {
				s.o.Log.Error("plane backup: nightly run failed", "error", err)
			}
		}
	}
}

// Verify proves an operator holds the Recovery Key, by decrypting the newest
// snapshot's manifest with the identity they paste in.
//
// It exists because "armed" and "recoverable" are different claims, and the
// panel must not make the second one on the strength of the first. The identity
// is used and discarded: it is never written to the row it verifies, never
// logged, and never returned.
func (s *Service) Verify(ctx context.Context, identity string, fetch func(ctx context.Context, target domain.BackupTarget, key string) ([]byte, error)) error {
	cfg, armed := s.Config(ctx)
	if !armed {
		return fmt.Errorf("planebackup: disaster recovery is not armed")
	}
	snaps, err := s.o.Store.ListPlaneSnapshots(ctx, 1)
	if err != nil || len(snaps) == 0 {
		return fmt.Errorf("planebackup: there is no snapshot to check against yet — take one first")
	}
	target, err := s.o.Store.GetBackupTarget(ctx, cfg.TargetID)
	if err != nil {
		return err
	}
	body, err := fetch(ctx, target, snaps[0].ObjectKey)
	if err != nil {
		return fmt.Errorf("planebackup: fetching the snapshot: %w", err)
	}
	if _, err := s.o.Enc.Unwrap(bytes.NewReader(body), identity); err != nil {
		return fmt.Errorf("planebackup: that key does not open this panel's snapshots")
	}
	return s.o.Store.MarkPlaneDRRecipientVerified(ctx, s.o.Now())
}

// Snapshots is the index.
func (s *Service) Snapshots(ctx context.Context, limit int) ([]domain.PlaneSnapshot, error) {
	return s.o.Store.ListPlaneSnapshots(ctx, limit)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
