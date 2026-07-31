// Package agentrpc implements the AgentService gRPC server that CypherAgents
// dial into. Transport security (mTLS) is configured by the caller; in
// production a valid agent client certificate is the authorization to talk
// here at all.
package agentrpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/dkim"
	"github.com/MaramHarsha/CypherPanel/internal/dns"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/store"
	"github.com/MaramHarsha/CypherPanel/internal/version"
)

type Server struct {
	agentv1.UnimplementedAgentServiceServer
	Servers   *store.Servers
	Tasks     *store.Tasks
	Accounts  *store.Accounts
	Databases *store.Databases
	FTP       *store.FTPAccounts
	Mail      *store.MailAccounts
	Backups   *store.Backups
	Events    *events.Bus
	Audit     *audit.Logger
	// DNS publishes records Core owns on behalf of agents — today the DKIM
	// public key an agent generates. Nil when DNS is not configured.
	DNS dns.Provider
	// Crypt encrypts secrets returned in task metadata (DB passwords) before
	// they are persisted, and decrypts backup-destination credentials when an
	// agent asks for them. Never nil in production wiring.
	Crypt interface {
		Encrypt([]byte) ([]byte, error)
		Decrypt([]byte) ([]byte, error)
	}
}

func (s *Server) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	if req.GetHostname() == "" || req.GetIpAddress() == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname and ip_address are required")
	}

	// Refuse an agent older than the supported minimum so a too-old node fails
	// loudly here rather than mysteriously mid-operation (compatibility matrix).
	if ok, reason := version.AgentCompatible(req.GetAgentVersion()); !ok {
		slog.Warn("rejecting incompatible agent", "hostname", req.GetHostname(), "agent_version", req.GetAgentVersion(), "reason", reason)
		return nil, status.Errorf(codes.FailedPrecondition, "incompatible agent: %s (Core %s requires agent >= %s)", reason, version.Core, version.MinAgent)
	} else if reason != "" {
		slog.Warn("agent registered with a non-release version", "hostname", req.GetHostname(), "agent_version", req.GetAgentVersion(), "note", reason)
	}

	srv, err := s.Servers.UpsertByHostname(ctx, req.GetHostname(), req.GetIpAddress(), req.GetRegion())
	if err != nil {
		slog.Error("registering agent", "hostname", req.GetHostname(), "error", err)
		return nil, status.Error(codes.Internal, "registration failed")
	}

	_ = s.Audit.Record(ctx, audit.Entry{
		ActorRole: "agent", Action: "server.register", TargetType: "server", TargetID: srv.ID,
		Detail: map[string]any{
			"hostname":      req.GetHostname(),
			"agent_version": req.GetAgentVersion(),
			"distro_family": req.GetDistroFamily(),
		},
		IP: req.GetIpAddress(),
	})

	s.Events.Publish(ctx, events.SubjectServerRegistered, "server", srv.ID, map[string]any{
		"id": srv.ID, "hostname": srv.Hostname, "distro_family": req.GetDistroFamily(),
	})

	slog.Info("agent registered", "server_id", srv.ID, "hostname", srv.Hostname, "distro", req.GetDistroFamily())
	return &agentv1.RegisterResponse{ServerId: srv.ID}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	if req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	stats := store.HostStats{
		Load1m:           req.GetStats().GetLoad_1M(),
		MemoryTotalBytes: req.GetStats().GetMemoryTotalBytes(),
		MemoryUsedBytes:  req.GetStats().GetMemoryUsedBytes(),
		DiskTotalBytes:   req.GetStats().GetDiskTotalBytes(),
		DiskUsedBytes:    req.GetStats().GetDiskUsedBytes(),
	}
	svcs := make([]store.ServiceStatus, 0, len(req.GetServices()))
	for _, s := range req.GetServices() {
		svcs = append(svcs, store.ServiceStatus{Name: s.GetName(), State: s.GetState()})
	}
	if err := s.Servers.Heartbeat(ctx, req.GetServerId(), stats, svcs); err != nil {
		if err == store.ErrNotFound {
			// Unknown ID: tell the agent to re-register (e.g. server row was
			// deleted from the panel).
			return nil, status.Error(codes.NotFound, "unknown server_id; re-register")
		}
		slog.Error("heartbeat", "server_id", req.GetServerId(), "error", err)
		return nil, status.Error(codes.Internal, "heartbeat failed")
	}
	return &agentv1.HeartbeatResponse{}, nil
}

func (s *Server) ReportTaskResult(ctx context.Context, req *agentv1.ReportTaskResultRequest) (*agentv1.ReportTaskResultResponse, error) {
	if req.GetTaskId() == "" {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}

	result := "failed"
	if req.GetStatus() == agentv1.TaskStatus_TASK_STATUS_SUCCEEDED {
		result = "succeeded"
	}
	if err := s.Tasks.SetResult(ctx, req.GetTaskId(), result, req.GetErrorMessage()); err != nil {
		if err == store.ErrNotFound {
			return nil, status.Error(codes.NotFound, "unknown task_id")
		}
		slog.Error("recording task result", "task_id", req.GetTaskId(), "error", err)
		return nil, status.Error(codes.Internal, "could not record result")
	}

	_ = s.Audit.Record(ctx, audit.Entry{
		ActorRole: "agent", Action: "task.result", TargetType: "task", TargetID: req.GetTaskId(),
		Detail: map[string]any{
			"server_id": req.GetServerId(),
			"status":    result,
			"error":     req.GetErrorMessage(),
		},
	})

	s.applyAccountTransition(ctx, req.GetTaskId(), result, req.GetMetadata())
	return &agentv1.ReportTaskResultResponse{}, nil
}

// applyAccountTransition drives account lifecycle from provisioning task
// outcomes: create-success → active, create-failure → failed,
// remove-success → account (and its panel user) deleted.
func (s *Server) applyAccountTransition(ctx context.Context, taskID, result string, meta map[string]string) {
	task, err := s.Tasks.GetByID(ctx, taskID)
	if err != nil || task.AccountID == "" {
		return
	}

	// SSL issuance drives ssl_status, not account status.
	if task.Type == "ssl.issue" {
		s.applySSLResult(ctx, task.AccountID, result, meta)
		return
	}
	// Database tasks drive the database record, keyed by account + name.
	if task.Type == "db.create" || task.Type == "db.drop" {
		s.applyDBResult(ctx, task, result, meta)
		return
	}
	// FTP tasks drive the ftp_account record, keyed by username.
	if task.Type == "ftp.create" || task.Type == "ftp.delete" {
		s.applyFTPResult(ctx, task, result, meta)
		return
	}
	// Mail tasks drive the mail_account record, keyed by address.
	if task.Type == "mail.create" || task.Type == "mail.delete" {
		s.applyMailResult(ctx, task, result, meta)
		return
	}
	// Backup/restore tasks close out their account_backups row.
	if task.Type == jobs.TypeBackupRun || task.Type == jobs.TypeBackupRestore {
		s.applyBackupResult(ctx, task, result, meta)
		return
	}

	var terr error
	var subject string
	switch {
	case task.Type == "system_user.create" && result == "succeeded":
		terr = s.Accounts.SetStatus(ctx, task.AccountID, "active")
		subject = events.SubjectAccountActivated
	case task.Type == "system_user.create" && result == "failed":
		terr = s.Accounts.SetStatus(ctx, task.AccountID, "failed")
		subject = events.SubjectAccountFailed
	case task.Type == "system_user.remove" && result == "succeeded":
		terr = s.Accounts.Delete(ctx, task.AccountID)
		subject = events.SubjectAccountTerminated
	default:
		return
	}
	if terr != nil && terr != store.ErrNotFound {
		slog.Error("applying account transition", "task_id", taskID, "account_id", task.AccountID, "error", terr)
		return
	}
	s.Events.Publish(ctx, subject, "account", task.AccountID, map[string]any{"id": task.AccountID})
}

// applyDBResult applies a database task's outcome to its record. The record is
// found by (account, name) from the task payload. On successful create the
// generated password (in result metadata) is encrypted before storage; it is
// never logged.
func (s *Server) applyDBResult(ctx context.Context, task *store.Task, result string, meta map[string]string) {
	var name string
	switch task.Type {
	case "db.create":
		var p jobs.DBCreatePayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return
		}
		name = p.Name
	case "db.drop":
		var p jobs.DBDropPayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return
		}
		name = p.Name
	}

	rec, err := s.Databases.GetByAccountAndName(ctx, task.AccountID, name)
	if err != nil {
		return
	}

	if task.Type == "db.create" {
		if result != "succeeded" {
			_ = s.Databases.SetStatus(ctx, rec.ID, "failed")
			return
		}
		var enc []byte
		if pw := meta[jobs.MetaDBPassword]; pw != "" && s.Crypt != nil {
			if e, cerr := s.Crypt.Encrypt([]byte(pw)); cerr == nil {
				enc = e
			}
		}
		if err := s.Databases.SetActive(ctx, rec.ID, enc); err != nil {
			slog.Error("activating database", "db_id", rec.ID, "error", err)
		}
		return
	}

	// db.drop: remove the record on success, mark failed otherwise.
	if result == "succeeded" {
		_ = s.Databases.Delete(ctx, rec.ID)
	} else {
		_ = s.Databases.SetStatus(ctx, rec.ID, "failed")
	}
}

// applyFTPResult applies an FTP task's outcome to its record (keyed by the
// username in the payload). On successful create the generated password is
// encrypted and the agent-derived home dir stored; never logged.
func (s *Server) applyFTPResult(ctx context.Context, task *store.Task, result string, meta map[string]string) {
	var username string
	switch task.Type {
	case "ftp.create":
		var p jobs.FTPCreatePayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return
		}
		username = p.Username
	case "ftp.delete":
		var p jobs.FTPDeletePayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return
		}
		username = p.Username
	}

	rec, err := s.FTP.GetByUsername(ctx, username)
	if err != nil {
		return
	}

	if task.Type == "ftp.create" {
		if result != "succeeded" {
			_ = s.FTP.SetStatus(ctx, rec.ID, "failed")
			return
		}
		var enc []byte
		if pw := meta[jobs.MetaFTPPassword]; pw != "" && s.Crypt != nil {
			if e, cerr := s.Crypt.Encrypt([]byte(pw)); cerr == nil {
				enc = e
			}
		}
		if err := s.FTP.SetActive(ctx, rec.ID, meta[jobs.MetaFTPHome], enc); err != nil {
			slog.Error("activating ftp account", "ftp_id", rec.ID, "error", err)
		}
		return
	}

	if result == "succeeded" {
		_ = s.FTP.Delete(ctx, rec.ID)
	} else {
		_ = s.FTP.SetStatus(ctx, rec.ID, "failed")
	}
}

// applyMailResult applies a mail task's outcome to its record (keyed by the
// address in the payload): create-success → active, create-failure → failed,
// delete-success → removed.
func (s *Server) applyMailResult(ctx context.Context, task *store.Task, result string, meta map[string]string) {
	var address, domain string
	switch task.Type {
	case "mail.create":
		var p jobs.MailCreatePayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return
		}
		address, domain = p.Address, p.Domain
	case "mail.delete":
		var p jobs.MailDeletePayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return
		}
		address = p.Address
	}
	rec, err := s.Mail.GetByAddress(ctx, address)
	if err != nil {
		return
	}
	if task.Type == "mail.create" {
		if result == "succeeded" {
			_ = s.Mail.SetStatus(ctx, rec.ID, "active")
			// The agent generated (or reused) the domain's DKIM key and
			// returned only the public half; publish it so outbound mail can
			// actually be verified.
			s.publishDKIM(ctx, domain, meta)
		} else {
			_ = s.Mail.SetStatus(ctx, rec.ID, "failed")
		}
		return
	}
	if result == "succeeded" {
		_ = s.Mail.Delete(ctx, rec.ID)
	} else {
		_ = s.Mail.SetStatus(ctx, rec.ID, "failed")
	}
}

// applyBackupResult closes out the account_backups row a task belongs to.
// Failures are recorded with their message, not dropped — a backup history
// that only shows successes hides the one thing an operator must know.
func (s *Server) applyBackupResult(ctx context.Context, task *store.Task, result string, meta map[string]string) {
	if s.Backups == nil {
		return
	}
	run, err := s.Backups.GetRunByTask(ctx, task.ID)
	if err != nil {
		return
	}
	if result != "succeeded" {
		msg := task.Error
		if msg == "" {
			msg = "task failed"
		}
		_ = s.Backups.CompleteRun(ctx, run.ID, "", 0, msg)
		return
	}

	snapshotID := meta[jobs.MetaBackupSnapshotID]
	var size int64
	if v := meta[jobs.MetaBackupSizeBytes]; v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			size = n
		}
	}
	if err := s.Backups.CompleteRun(ctx, run.ID, snapshotID, size, ""); err != nil {
		slog.Error("completing backup run", "backup_id", run.ID, "error", err)
	}
}

// publishDKIM publishes a domain's DKIM public key as its `_domainkey` TXT
// record. Best-effort: mail is already provisioned, and a DNS blip must not
// fail the mailbox — the record is republished on the next mailbox creation.
func (s *Server) publishDKIM(ctx context.Context, domain string, meta map[string]string) {
	pub := meta[jobs.MetaDKIMPublicTXT]
	if s.DNS == nil || domain == "" || pub == "" {
		return
	}
	selector := meta[jobs.MetaDKIMSelector]
	if selector == "" {
		selector = dkim.DefaultSelector
	}
	// A 2048-bit key overflows a single 255-byte DNS character-string, so the
	// value must be published as multiple quoted chunks.
	rec := dns.Record{
		Name:     dkim.RecordName(domain, selector),
		Type:     "TXT",
		TTL:      3600,
		Contents: []string{dkim.SplitTXT(pub)},
	}
	if err := s.DNS.UpsertRecord(ctx, domain, rec); err != nil {
		slog.Warn("publishing DKIM record", "domain", domain, "selector", selector, "error", err)
		return
	}
	slog.Info("published DKIM record", "domain", domain, "selector", selector)
}

// FetchBackupCredentials releases one destination's secrets to an agent.
//
// Authorization is the mTLS client certificate (an agent) plus a re-check that
// the named task really belongs to the calling server and really references
// the requested destination — so a compromised agent cannot enumerate
// credentials for destinations it was never asked to write to.
func (s *Server) FetchBackupCredentials(ctx context.Context, req *agentv1.FetchBackupCredentialsRequest) (*agentv1.FetchBackupCredentialsResponse, error) {
	if req.GetServerId() == "" || req.GetTaskId() == "" || req.GetDestinationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id, task_id and destination_id are required")
	}
	if s.Backups == nil || s.Crypt == nil {
		return nil, status.Error(codes.FailedPrecondition, "backups are not configured")
	}

	task, err := s.Tasks.GetByID(ctx, req.GetTaskId())
	if err != nil {
		return nil, status.Error(codes.NotFound, "unknown task_id")
	}
	if task.ServerID != req.GetServerId() {
		return nil, status.Error(codes.PermissionDenied, "task does not belong to this server")
	}
	if task.Type != jobs.TypeBackupRun && task.Type != jobs.TypeBackupRestore {
		return nil, status.Error(codes.PermissionDenied, "task is not a backup task")
	}
	// The destination must be the one this task was created for.
	var payloadDest string
	switch task.Type {
	case jobs.TypeBackupRun:
		var p jobs.BackupRunPayload
		if json.Unmarshal(task.Payload, &p) == nil {
			payloadDest = p.DestinationID
		}
	case jobs.TypeBackupRestore:
		var p jobs.BackupRestorePayload
		if json.Unmarshal(task.Payload, &p) == nil {
			payloadDest = p.DestinationID
		}
	}
	if payloadDest == "" || payloadDest != req.GetDestinationId() {
		return nil, status.Error(codes.PermissionDenied, "task does not reference this destination")
	}

	dest, err := s.Backups.GetDestination(ctx, req.GetDestinationId())
	if err != nil {
		return nil, status.Error(codes.NotFound, "unknown destination")
	}
	plain, err := s.Crypt.Decrypt(dest.CredentialsEncrypted)
	if err != nil {
		slog.Error("decrypting backup credentials", "destination_id", dest.ID, "error", err)
		return nil, status.Error(codes.Internal, "could not read destination credentials")
	}
	var creds struct {
		Password string            `json:"password"`
		Env      map[string]string `json:"env,omitempty"`
	}
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, status.Error(codes.Internal, "malformed destination credentials")
	}

	// Audit the release of credentials — but never the credentials themselves.
	_ = s.Audit.Record(ctx, audit.Entry{
		ActorRole: "agent", Action: "backup.credentials.fetch",
		TargetType: "backup_destination", TargetID: dest.ID,
		Detail: map[string]any{"server_id": req.GetServerId(), "task_id": req.GetTaskId()},
	})

	return &agentv1.FetchBackupCredentialsResponse{
		Repository: dest.Repository,
		Password:   creds.Password,
		Env:        creds.Env,
	}, nil
}

func (s *Server) applySSLResult(ctx context.Context, accountID, result string, meta map[string]string) {
	if result != "succeeded" {
		_ = s.Accounts.SetSSL(ctx, accountID, "failed", nil)
		return
	}
	var expires *time.Time
	if v := meta["ssl_not_after"]; v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			expires = &t
		}
	}
	if err := s.Accounts.SetSSL(ctx, accountID, "active", expires); err != nil && err != store.ErrNotFound {
		slog.Error("applying ssl result", "account_id", accountID, "error", err)
		return
	}
	s.Events.Publish(ctx, events.SubjectAccountSSLIssued, "account", accountID, map[string]any{"id": accountID})
}
