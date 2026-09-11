package grpcapi

// RelayServer authorization tests (builder-role-and-relay.md §5): transfers
// are authorized per-deployment against the persisted builder/target from
// the verified client certificate CN — never from what the caller claims —
// and a no-longer-needed push completes immediately without a session.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/relay"
	"github.com/MaramHarsha/cypherpanel/core/store"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

type fakeRelayStore struct {
	dep domain.Deployment
	app domain.Application
}

func (f *fakeRelayStore) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	if id != f.dep.ID {
		return domain.Deployment{}, store.ErrNotFound
	}
	return f.dep, nil
}

func (f *fakeRelayStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	if id != f.app.ID {
		return domain.Application{}, store.ErrNotFound
	}
	return f.app, nil
}

// agentCtx is a context carrying a verified client certificate for serverID,
// exactly as the mTLS listener would present it.
func agentCtx(serverID string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: serverID}}}},
		}},
	})
}

type fakePushStream struct {
	grpc.ServerStream
	ctx    context.Context
	chunks []*agentv1.PushImageRequest
	i      int
	mu     sync.Mutex
	resp   *agentv1.PushImageResponse
}

func (s *fakePushStream) Context() context.Context { return s.ctx }
func (s *fakePushStream) Recv() (*agentv1.PushImageRequest, error) {
	if s.i >= len(s.chunks) {
		return nil, io.EOF
	}
	c := s.chunks[s.i]
	s.i++
	return c, nil
}
func (s *fakePushStream) SendAndClose(r *agentv1.PushImageResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resp = r
	return nil
}
func (s *fakePushStream) gotResponse() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resp != nil
}

type fakePullStream struct {
	grpc.ServerStream
	ctx  context.Context
	mu   sync.Mutex
	data []byte
}

func (s *fakePullStream) Context() context.Context { return s.ctx }
func (s *fakePullStream) Send(r *agentv1.PullImageResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append(s.data, r.GetData()...)
	return nil
}
func (s *fakePullStream) received() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}

func relayFixture(depStatus domain.DeploymentStatus) (*RelayServer, *fakeRelayStore) {
	builder := "srv_b"
	st := &fakeRelayStore{
		dep: domain.Deployment{ID: "dep1", ApplicationID: "app1", Status: depStatus, BuilderServerID: &builder},
		app: domain.Application{ID: "app1", Runtime: domain.AppRuntime{ServerID: "srv_w"}},
	}
	srv := NewRelayServer(relay.New(2*time.Second), st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return srv, st
}

func pushChunks(dep string, payload []byte) []*agentv1.PushImageRequest {
	return []*agentv1.PushImageRequest{{DeploymentId: dep, Data: payload}}
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("err = %v (code %s), want %s", err, status.Code(err), want)
	}
}

func TestPushWithoutClientCertDenied(t *testing.T) {
	srv, _ := relayFixture(domain.DeployDistributing)
	err := srv.PushImage(&fakePushStream{ctx: context.Background(), chunks: pushChunks("dep1", []byte("x"))})
	wantCode(t, err, codes.PermissionDenied)
}

func TestPushFromNonBuilderDenied(t *testing.T) {
	srv, _ := relayFixture(domain.DeployDistributing)
	err := srv.PushImage(&fakePushStream{ctx: agentCtx("srv_evil"), chunks: pushChunks("dep1", []byte("x"))})
	wantCode(t, err, codes.PermissionDenied)
}

// The target itself may not push — only the recorded builder holds the image
// of record (spec §5).
func TestPushFromTargetDenied(t *testing.T) {
	srv, _ := relayFixture(domain.DeployDistributing)
	err := srv.PushImage(&fakePushStream{ctx: agentCtx("srv_w"), chunks: pushChunks("dep1", []byte("x"))})
	wantCode(t, err, codes.PermissionDenied)
}

func TestPullFromNonTargetDenied(t *testing.T) {
	srv, _ := relayFixture(domain.DeployDistributing)
	err := srv.PullImage(&agentv1.PullImageRequest{DeploymentId: "dep1"}, &fakePullStream{ctx: agentCtx("srv_b")})
	wantCode(t, err, codes.PermissionDenied)
}

// A push for a deployment past distributing answers OK immediately: the
// builder acks its item, no session is formed (spec §3).
func TestPushAfterDistributingCompletesImmediately(t *testing.T) {
	srv, _ := relayFixture(domain.DeployRollingOut)
	stream := &fakePushStream{ctx: agentCtx("srv_b"), chunks: pushChunks("dep1", []byte("x"))}
	if err := srv.PushImage(stream); err != nil {
		t.Fatalf("PushImage: %v", err)
	}
	if !stream.gotResponse() {
		t.Fatal("no OK response on the not-needed path")
	}
}

func TestPullWhenNotDistributingRejected(t *testing.T) {
	srv, _ := relayFixture(domain.DeployRollingOut)
	err := srv.PullImage(&agentv1.PullImageRequest{DeploymentId: "dep1"}, &fakePullStream{ctx: agentCtx("srv_w")})
	wantCode(t, err, codes.FailedPrecondition)
}

func TestUnknownDeploymentIsNotFound(t *testing.T) {
	srv, _ := relayFixture(domain.DeployDistributing)
	err := srv.PullImage(&agentv1.PullImageRequest{DeploymentId: "dep_gone"}, &fakePullStream{ctx: agentCtx("srv_w")})
	wantCode(t, err, codes.NotFound)
}

// The authorized pair relays the exact bytes end to end.
func TestAuthorizedPushPullRelaysBytes(t *testing.T) {
	srv, _ := relayFixture(domain.DeployDistributing)
	payload := bytes.Repeat([]byte("image-tar-"), 1000)

	var wg sync.WaitGroup
	var pushErr, pullErr error
	pull := &fakePullStream{ctx: agentCtx("srv_w")}
	wg.Add(2)
	go func() {
		defer wg.Done()
		pushErr = srv.PushImage(&fakePushStream{ctx: agentCtx("srv_b"), chunks: pushChunks("dep1", payload)})
	}()
	go func() {
		defer wg.Done()
		pullErr = srv.PullImage(&agentv1.PullImageRequest{DeploymentId: "dep1"}, pull)
	}()
	wg.Wait()

	if pushErr != nil || pullErr != nil {
		t.Fatalf("push err = %v, pull err = %v", pushErr, pullErr)
	}
	if !bytes.Equal(pull.received(), payload) {
		t.Fatalf("relayed %d bytes, want %d intact", len(pull.received()), len(payload))
	}
}

// A puller alone times out with a retryable code — the work item NAKs and
// redelivers (spec §6).
func TestPullAloneTimesOutRetryable(t *testing.T) {
	builder := "srv_b"
	st := &fakeRelayStore{
		dep: domain.Deployment{ID: "dep1", ApplicationID: "app1", Status: domain.DeployDistributing, BuilderServerID: &builder},
		app: domain.Application{ID: "app1", Runtime: domain.AppRuntime{ServerID: "srv_w"}},
	}
	srv := NewRelayServer(relay.New(30*time.Millisecond), st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := srv.PullImage(&agentv1.PullImageRequest{DeploymentId: "dep1"}, &fakePullStream{ctx: agentCtx("srv_w")})
	wantCode(t, err, codes.DeadlineExceeded)
}
