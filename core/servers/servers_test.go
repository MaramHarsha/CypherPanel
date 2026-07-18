package servers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

type fakeStore struct {
	created struct {
		serverID, name, tokenID string
		tokenHash               []byte
		tokenExpiresAt          time.Time
	}
	deleted   []string
	deleteErr error
}

func (f *fakeStore) CreateServerWithToken(_ context.Context, serverID, name, tokenID string, tokenHash []byte, tokenExpiresAt time.Time) (domain.Server, error) {
	f.created.serverID = serverID
	f.created.name = name
	f.created.tokenID = tokenID
	f.created.tokenHash = tokenHash
	f.created.tokenExpiresAt = tokenExpiresAt
	return domain.Server{ID: serverID, Name: name, Status: domain.StatusUnknown}, nil
}

func (f *fakeStore) ListServers(context.Context) ([]domain.Server, error) { return nil, nil }

func (f *fakeStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	return domain.Server{ID: id}, nil
}

func (f *fakeStore) DeleteServer(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

type fakeDisconnector struct {
	disconnected []string
	err          error
}

func (f *fakeDisconnector) DisconnectAgent(serverID string) error {
	f.disconnected = append(f.disconnected, serverID)
	return f.err
}

func newTestService(st *fakeStore, disc *fakeDisconnector) *Service {
	return NewService(st, disc, 15*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCreateValidatesName(t *testing.T) {
	svc := newTestService(&fakeStore{}, &fakeDisconnector{})
	for _, name := range []string{"", "   ", strings.Repeat("x", 101)} {
		if _, _, err := svc.Create(context.Background(), name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Create(%q): got %v, want ErrInvalidName", name, err)
		}
	}
}

func TestCreateIssuesJoinToken(t *testing.T) {
	st := &fakeStore{}
	svc := newTestService(st, &fakeDisconnector{})
	srv, token, err := svc.Create(context.Background(), "  web-1  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if srv.Name != "web-1" {
		t.Errorf("name not trimmed: %q", srv.Name)
	}
	if token == "" {
		t.Fatal("no raw join token returned")
	}
	if len(st.created.tokenHash) == 0 {
		t.Error("no token hash persisted")
	}
	// The raw secret must never be persisted — only its hash.
	if strings.Contains(token, string(st.created.tokenHash)) {
		t.Error("raw token appears in persisted material")
	}
}

// TestDeleteSeversAgentConnection: deleting a server must revoke its live bus
// connection, not just its row (threat-model §8 req 6).
func TestDeleteSeversAgentConnection(t *testing.T) {
	st := &fakeStore{}
	disc := &fakeDisconnector{}
	svc := newTestService(st, disc)
	if err := svc.Delete(context.Background(), "srv_gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(st.deleted) != 1 || st.deleted[0] != "srv_gone" {
		t.Errorf("store deletions = %v, want [srv_gone]", st.deleted)
	}
	if len(disc.disconnected) != 1 || disc.disconnected[0] != "srv_gone" {
		t.Errorf("disconnections = %v, want [srv_gone]", disc.disconnected)
	}
}

// TestDeleteDisconnectFailureIsNotFatal: once the row is gone the identity is
// revoked at the bus; a disconnect error is logged, not surfaced.
func TestDeleteDisconnectFailureIsNotFatal(t *testing.T) {
	disc := &fakeDisconnector{err: errors.New("bus down")}
	svc := newTestService(&fakeStore{}, disc)
	if err := svc.Delete(context.Background(), "srv_gone"); err != nil {
		t.Fatalf("Delete surfaced best-effort disconnect error: %v", err)
	}
}

// TestDeleteStoreFailureSkipsDisconnect: if the row was not deleted, the server
// is not revoked and its connection must not be cut.
func TestDeleteStoreFailureSkipsDisconnect(t *testing.T) {
	st := &fakeStore{deleteErr: errors.New("db down")}
	disc := &fakeDisconnector{}
	svc := newTestService(st, disc)
	if err := svc.Delete(context.Background(), "srv_kept"); err == nil {
		t.Fatal("Delete did not surface store error")
	}
	if len(disc.disconnected) != 0 {
		t.Errorf("disconnected %v despite failed delete", disc.disconnected)
	}
}
