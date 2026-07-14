// Package agentrpc implements the AgentService gRPC server that CypherAgents
// dial into. Transport security (mTLS) is configured by the caller; in
// production a valid agent client certificate is the authorization to talk
// here at all.
package agentrpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type Server struct {
	agentv1.UnimplementedAgentServiceServer
	Servers  *store.Servers
	Tasks    *store.Tasks
	Accounts *store.Accounts
	Events   *events.Bus
	Audit    *audit.Logger
}

func (s *Server) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	if req.GetHostname() == "" || req.GetIpAddress() == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname and ip_address are required")
	}

	srv, err := s.Servers.UpsertByHostname(ctx, req.GetHostname(), req.GetIpAddress())
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
