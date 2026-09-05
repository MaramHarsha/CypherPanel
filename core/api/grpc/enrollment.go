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
	"google.golang.org/protobuf/types/known/timestamppb"

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

// Renew re-signs the caller's own certificate before it expires
// (agent-identity-and-tls.md §3). Authorization is the verified client
// certificate on the connection — the same identity NATS trusts — never
// anything in the request body: callerServerID returns PermissionDenied when
// the peer presented no verified chain, which is exactly what an agent that has
// not enrolled (or whose certificate has already expired) looks like.
func (s *EnrollmentServer) Renew(ctx context.Context, req *agentv1.RenewRequest) (*agentv1.RenewResponse, error) {
	caller, err := callerServerID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.svc.Renew(ctx, caller, req.GetServerId(), req.GetCsrPem(), req.GetAgentVersion())
	switch {
	case errors.Is(err, enroll.ErrUnknownIdentity):
		// A revoked or deleted server. Logged at warn with the id: an agent
		// still trying to renew after revocation is worth seeing, and the id is
		// the plane's own, not a secret.
		s.log.Warn("certificate renewal refused: unknown or revoked identity", "server_id", caller)
		return nil, status.Error(codes.PermissionDenied, "unknown or revoked agent identity")
	case errors.Is(err, enroll.ErrIdentityMismatch):
		s.log.Warn("certificate renewal refused: identity mismatch",
			"server_id", caller, "claimed_server_id", req.GetServerId())
		return nil, status.Error(codes.PermissionDenied, "renewal request does not match the caller's certificate")
	case err != nil:
		s.log.Error("certificate renewal failed", "server_id", caller, "error", err)
		return nil, status.Error(codes.Internal, "certificate renewal failed")
	}
	s.log.Info("agent certificate renewed",
		"server_id", res.ServerID,
		"agent_version", req.GetAgentVersion(),
		"not_after", res.NotAfter,
	)
	return &agentv1.RenewResponse{
		CertificatePem: res.CertPEM,
		CaPem:          res.CACertPEM,
		NotAfter:       timestamppb.New(res.NotAfter),
	}, nil
}
