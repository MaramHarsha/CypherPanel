package grpcapi

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/relay"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// relayChunkSize bounds one pull chunk; pushes are bounded by the sender
// (spec §3: ≤1 MiB either way, so flow control is the memory bound).
const relayChunkSize = 1 << 20

// RelayStore is the persistence the relay needs to authorize a transfer
// (consumer-defined; *store.Store satisfies it).
type RelayStore interface {
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	GetApplication(ctx context.Context, id string) (domain.Application, error)
}

// RelayServer serves ImageRelayService on the enrollment listener. Both RPCs
// require a verified agent client certificate; every transfer is authorized
// per-deployment against the persisted builder/target — never against what
// the caller claims (builder-role-and-relay.md §5).
type RelayServer struct {
	agentv1.UnimplementedImageRelayServiceServer
	relay *relay.Relay
	store RelayStore
	log   *slog.Logger
}

// NewRelayServer wires the relay adapter.
func NewRelayServer(r *relay.Relay, st RelayStore, log *slog.Logger) *RelayServer {
	return &RelayServer{relay: r, store: st, log: log}
}

// callerServerID is the verified client certificate's CN — the same identity
// NATS trusts (ADR-002). No certificate (or an unverified one) means the
// caller may enroll, but never relay.
func callerServerID(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", status.Error(codes.PermissionDenied, "client certificate required")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return "", status.Error(codes.PermissionDenied, "client certificate required")
	}
	return tlsInfo.State.VerifiedChains[0][0].Subject.CommonName, nil
}

// deploymentParties resolves who may push and pull for a deployment.
func (s *RelayServer) deploymentParties(ctx context.Context, deploymentID string) (dep domain.Deployment, builderID, targetID string, err error) {
	dep, err = s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return dep, "", "", status.Error(codes.NotFound, "unknown deployment")
	}
	app, err := s.store.GetApplication(ctx, dep.ApplicationID)
	if err != nil {
		return dep, "", "", status.Error(codes.NotFound, "unknown application")
	}
	targetID = app.Runtime.ServerID
	builderID = targetID
	if dep.BuilderServerID != nil {
		builderID = *dep.BuilderServerID
	}
	return dep, builderID, targetID, nil
}

// PushImage receives the builder's image tar and feeds it to the rendezvous.
func (s *RelayServer) PushImage(stream agentv1.ImageRelayService_PushImageServer) error {
	ctx := stream.Context()
	caller, err := callerServerID(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "empty push stream")
	}
	depID := first.GetDeploymentId()
	dep, builderID, _, err := s.deploymentParties(ctx, depID)
	if err != nil {
		return err
	}
	if caller != builderID {
		s.log.Warn("relay push from a server that is not the deployment's builder",
			"deployment_id", depID, "caller", caller, "builder", builderID)
		return status.Error(codes.PermissionDenied, "not this deployment's builder")
	}
	if dep.Status != domain.DeployDistributing {
		// The target already has the image (or the deployment died): nothing
		// to relay. Answer OK immediately so the builder acks its item.
		return stream.SendAndClose(&agentv1.PushImageResponse{})
	}

	w, err := s.relay.Push(ctx, depID)
	if err != nil {
		return relayErr(err)
	}
	if len(first.GetData()) > 0 {
		if _, err := w.Write(first.GetData()); err != nil {
			s.relay.Drop(depID, err)
			return relayErr(err)
		}
	}
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = w.Close() // clean end-of-stream to the puller
			return stream.SendAndClose(&agentv1.PushImageResponse{})
		}
		if err != nil {
			s.relay.Drop(depID, err)
			return relayErr(err)
		}
		if _, err := w.Write(chunk.GetData()); err != nil {
			s.relay.Drop(depID, err)
			return relayErr(err)
		}
	}
}

// PullImage streams the relayed tar out to the deployment's target.
func (s *RelayServer) PullImage(req *agentv1.PullImageRequest, stream agentv1.ImageRelayService_PullImageServer) error {
	ctx := stream.Context()
	caller, err := callerServerID(ctx)
	if err != nil {
		return err
	}
	depID := req.GetDeploymentId()
	dep, _, targetID, err := s.deploymentParties(ctx, depID)
	if err != nil {
		return err
	}
	if caller != targetID {
		s.log.Warn("relay pull from a server that is not the deployment's target",
			"deployment_id", depID, "caller", caller, "target", targetID)
		return status.Error(codes.PermissionDenied, "not this deployment's target")
	}
	if dep.Status != domain.DeployDistributing {
		return status.Error(codes.FailedPrecondition, "deployment is not distributing")
	}

	r, err := s.relay.Pull(ctx, depID)
	if err != nil {
		return relayErr(err)
	}
	buf := make([]byte, relayChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if serr := stream.Send(&agentv1.PullImageResponse{Data: buf[:n]}); serr != nil {
				s.relay.Drop(depID, serr)
				return relayErr(serr)
			}
		}
		if errors.Is(err, io.EOF) {
			s.relay.Drop(depID, nil)
			return nil
		}
		if err != nil {
			s.relay.Drop(depID, err)
			return relayErr(err)
		}
	}
}

// relayErr maps rendezvous conditions onto retryable gRPC codes: the agent
// NAKs and the work item redelivers with a fresh session (spec §6).
func relayErr(err error) error {
	switch {
	case errors.Is(err, relay.ErrBusy):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, relay.ErrPeerTimeout):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Unavailable, err.Error())
	}
}
