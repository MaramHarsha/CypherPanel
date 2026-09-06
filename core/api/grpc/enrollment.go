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
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/enroll"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// AuditRecorder records a server joining the fleet (consumer-defined;
// *audit.Service satisfies it). The threat model requires enrollment to be "a
// first-class, audited event surfaced in the UI, so rogue enrollment isn't
// silent" (§5.3, §8.1) — before this it was one slog line.
//
// nil records nothing, which keeps enrollment working on a panel wired without
// the audit log.
type AuditRecorder interface {
	Record(ctx context.Context, e audit.Entry) (domain.AuditEvent, error)
}

// EnrollmentServer adapts enroll.Service to the generated gRPC service.
type EnrollmentServer struct {
	agentv1.UnimplementedEnrollmentServiceServer
	svc   *enroll.Service
	audit AuditRecorder
	log   *slog.Logger
}

// NewEnrollmentServer wires the gRPC adapter. rec may be nil, in which case
// enrollment is logged but not audited.
func NewEnrollmentServer(svc *enroll.Service, rec AuditRecorder, log *slog.Logger) *EnrollmentServer {
	return &EnrollmentServer{svc: svc, audit: rec, log: log}
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
	// The actor is the AGENT, not a person: nobody was signed in, and the join
	// token that authorised this was handed out by the operator who created the
	// server — an action already in the log as server.created. The two rows
	// together are the whole story of a host joining the fleet.
	s.record(ctx, audit.Entry{
		Action: audit.ActionServerEnrolled,
		Actor: domain.AuditActor{
			Kind:  domain.AuditActorAgent,
			Label: req.GetHostname(),
		},
		Resource: audit.Resource(audit.ResourceServer, res.ServerID, req.GetHostname()),
		Detail: map[string]any{
			"hostname":      req.GetHostname(),
			"agent_version": req.GetAgentVersion(),
		},
		ClientIP: peerAddr(ctx),
	})
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

// record writes one audit entry, never failing the RPC that produced it: the
// agent is already enrolled by the time this runs, and refusing the response
// would leave a host holding a certificate the plane says it never issued.
func (s *EnrollmentServer) record(ctx context.Context, e audit.Entry) {
	if s.audit == nil {
		return
	}
	if _, err := s.audit.Record(ctx, e); err != nil {
		s.log.Error("recording audit event", "action", e.Action, "server_id", e.Resource.ID, "error", err)
	}
}

// peerAddr is the address the agent dialled from. Unlike the REST side there is
// no trusted-proxy question here: this is a direct mTLS connection, so the TCP
// peer is the client by construction.
func peerAddr(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	return p.Addr.String()
}
