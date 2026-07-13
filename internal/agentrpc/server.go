// Package agentrpc implements the AgentService gRPC server that CypherAgents
// dial into. Transport security (mTLS) is configured by the caller; in
// production a valid agent client certificate is the authorization to talk
// here at all.
package agentrpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type Server struct {
	agentv1.UnimplementedAgentServiceServer
	Servers *store.Servers
	Audit   *audit.Logger
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

	slog.Info("agent registered", "server_id", srv.ID, "hostname", srv.Hostname, "distro", req.GetDistroFamily())
	return &agentv1.RegisterResponse{ServerId: srv.ID}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	if req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if err := s.Servers.Heartbeat(ctx, req.GetServerId()); err != nil {
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
	// Task persistence lands with the NATS job pipeline; record the outcome
	// in the audit trail so results are never silently dropped meanwhile.
	_ = s.Audit.Record(ctx, audit.Entry{
		ActorRole: "agent", Action: "task.result", TargetType: "task", TargetID: req.GetTaskId(),
		Detail: map[string]any{
			"server_id": req.GetServerId(),
			"status":    req.GetStatus().String(),
			"error":     req.GetErrorMessage(),
		},
	})
	return &agentv1.ReportTaskResultResponse{}, nil
}
