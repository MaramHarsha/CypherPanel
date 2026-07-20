package scheduler

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ReconcileDatabase is called when a Database is created, configured, started,
// stopped, or marked for deletion. It translates the desired state of the
// Database into NATS DbProvisionWork or DbRemoveWork items.
//
// Convergence contract (ENGINEERING 12, 13):
// Publishing is idempotent: same work payloads carry stable message IDs
// so JetStream deduplicates them inside the window, and agent reconcilers
// run no side effect if the container state matches.
func (s *Scheduler) ReconcileDatabase(ctx context.Context, dbID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileDatabaseLocked(ctx, dbID)
}

func (s *Scheduler) reconcileDatabaseLocked(ctx context.Context, dbID string) error {
	db, err := s.store.GetDatabase(ctx, dbID)
	if err != nil {
		return fmt.Errorf("scheduler: loading database: %w", err)
	}

	// Deletion path: emit DbRemoveWork.
	if db.PendingDelete {
		work := &agentv1.DbRemoveWork{
			IdempotencyKey: db.ID + "-remove",
			DbId:           db.ID,
			DeleteVolume:   db.DeleteVolume,
		}
		data, err := proto.Marshal(work)
		if err != nil {
			return fmt.Errorf("scheduler: marshaling remove work: %w", err)
		}
		// MsgID is db.ID + ".remove" to deduplicate
		return s.bus.PublishWork(ctx, subjects.DbRemove(db.ServerID), db.ID+".remove", data)
	}

	// Stopped path: emit DbRemoveWork (without delete_volume) to tear down
	// the container but keep the volume.
	if db.Status == domain.DbStopped {
		work := &agentv1.DbRemoveWork{
			IdempotencyKey: db.ID + "-stop",
			DbId:           db.ID,
			DeleteVolume:   false,
		}
		data, err := proto.Marshal(work)
		if err != nil {
			return fmt.Errorf("scheduler: marshaling stop work: %w", err)
		}
		return s.bus.PublishWork(ctx, subjects.DbRemove(db.ServerID), db.ID+".stop", data)
	}

	// Desired configuration.
	if db.DesiredRevisionID == nil {
		return nil // nothing to provision
	}
	rev, err := s.store.GetDatabaseRevision(ctx, *db.DesiredRevisionID)
	if err != nil {
		return fmt.Errorf("scheduler: loading revision: %w", err)
	}

	defaults := domain.EngineDefaults(db.Engine, db.Version)
	env := make(map[string]string)

	// Decrypt sealed password if it exists and engine requires/uses it.
	if db.RequirePassword && len(db.RootPasswordCT) > 0 {
		pwd, err := s.opener.Open(db.RootPasswordCT, db.RootPasswordNonce)
		if err != nil {
			return fmt.Errorf("scheduler: decrypting root password: %w", err)
		}
		if defaults.PasswordEnv != "" {
			env[defaults.PasswordEnv] = string(pwd)
		}
	}

	spec := &agentv1.DbSpec{
		DbId:          db.ID,
		EnvironmentId: db.EnvironmentID,
		RevisionId:    rev.ID,
		Engine:        string(db.Engine),
		Image:         defaults.Image,
		VolumeName:    db.VolumeName,
		DataPath:      db.DataPath,
		Network:       db.Network,
		Env:           env,
		HealthCmd:     defaults.HealthCmd,
	}

	if db.ExposePort != nil {
		spec.ExposePort = uint32(*db.ExposePort)
	}
	if db.CPULimit != nil {
		spec.CpuLimit = *db.CPULimit
	}
	if db.MemoryLimitMB != nil {
		spec.MemoryLimitMb = uint32(*db.MemoryLimitMB)
	}

	work := &agentv1.DbProvisionWork{
		IdempotencyKey: db.ID + "-" + rev.ID,
		Spec:           spec,
	}
	data, err := proto.Marshal(work)
	if err != nil {
		return fmt.Errorf("scheduler: marshaling provision work: %w", err)
	}
	return s.bus.PublishWork(ctx, subjects.DbProvision(db.ServerID), db.ID+".provision", data)
}

// HandleDbStatus processes observed state reports from agents. Success or
// error transitions are driven solely by these reports (ADR-005).
func (s *Scheduler) HandleDbStatus(ctx context.Context, serverID string, st *agentv1.DbStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.store.GetDatabase(ctx, st.GetDbId())
	if err != nil {
		return // Database already deleted or doesn't exist
	}

	// Threat model validation: only the server designated to host this database
	// may report its status.
	if db.ServerID != serverID {
		s.log.Warn("db status: received report from wrong server",
			"db_id", db.ID, "reported_by", serverID, "assigned_to", db.ServerID)
		return
	}

	observedAt := s.now()
	if ts := st.GetObservedAt(); ts != nil {
		observedAt = ts.AsTime()
	}

	// Record observed status in store.
	if err := s.store.SetDatabaseObservedStatus(ctx, db.ID, st.GetState(), st.GetDetail(), st.GetRevisionId(), observedAt); err != nil {
		s.log.Error("db status: recording observed status", "db_id", db.ID, "error", err)
		return
	}

	// Deletion closeout: if the database was pending delete, and the agent
	// reports that it is indeed stopped/removed, remove the database row from
	// the plane's store.
	if db.PendingDelete && st.GetState() == domain.DbStopped {
		s.log.Info("db status: agent confirmed removal, deleting row", "db_id", db.ID)
		if err := s.store.DeleteDatabase(ctx, db.ID); err != nil {
			s.log.Error("db status: deleting row after confirmation", "db_id", db.ID, "error", err)
		}
	}
}
