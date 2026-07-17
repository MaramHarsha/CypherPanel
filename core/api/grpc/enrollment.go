// Package grpcapi implements the agent-facing gRPC services. Phase 1 exposes
// only EnrollmentService, served over server-authenticated TLS and gated by
// join token (ADR-002). No arbitrary-command surface exists here — the entire
// agent-facing API is enrollment plus, later, streaming for logs/terminal
// (threat-model §5.1, requirement 4).
package grpcapi

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/MaramHarsha/cypherpanel/core/enroll"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// EnrollmentServer adapts enroll.Service to the generated gRPC service.
type EnrollmentServer struct {
	agentv1.UnimplementedEnrollmentServiceServer
	svc *enroll.Service
	log *slog.Logger
}

// NewEnrollmentServer wires the gRPC adapter.
func NewEnrollmentServer(svc *enroll.Service, log *slog.Logger) *EnrollmentServer {
	return &EnrollmentServer{svc: svc, log: log}
}

// Enroll handles an agent's enrollment request.
func (s *EnrollmentServer) Enroll(ctx context.Context, req *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	res, err := s.svc.Enroll(ctx, req.GetJoinToken(), req.GetCsrPem(), req.GetHostname(), req.GetAgentVersion())
	if err != nil {
		if errors.Is(err, enroll.ErrInvalidToken) {
			// Undifferentiated on purpose — never reveal which tokens exist
			// (threat-model §5.3). Not logged as an error: rejecting bad tokens
			// is normal operation, and logging every attempt invites log spam.
			return nil, status.Error(codes.PermissionDenied, "invalid or expired join token")
		}
		s.log.Error("enrollment failed", "hostname", req.GetHostname(), "error", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}
	s.log.Info("server enrolled",
		"server_id", res.ServerID,
		"hostname", req.GetHostname(),
		"agent_version", req.GetAgentVersion(),
	)
	return &agentv1.EnrollResponse{
		ServerId:       res.ServerID,
		CertificatePem: res.CertPEM,
		CaPem:          res.CACertPEM,
		NatsUrl:        res.NATSURL,
	}, nil
}
