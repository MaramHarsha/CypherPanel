package conn

import (
	"io"
	"log/slog"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/cypherpanel/agent/identity"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// TestBusOptionsUseThePerIdentityInboxPrefix: the plane grants this identity
// Subscribe on subjects.InboxForServer(id) only, so a connection that replied
// into the default "_INBOX" scope would never hear its own sync answer — and a
// shared prefix is the cross-agent read threat-model §5.2 forbids.
func TestBusOptionsUseThePerIdentityInboxPrefix(t *testing.T) {
	id := &identity.Identity{ServerID: "srv_alpha"}
	opts := nats.GetDefaultOptions()
	for _, o := range busOptions(id, nil, slog.New(slog.NewTextHandler(io.Discard, nil))) {
		if err := o(&opts); err != nil {
			t.Fatalf("applying option: %v", err)
		}
	}
	if opts.InboxPrefix != subjects.InboxPrefix("srv_alpha") {
		t.Fatalf("InboxPrefix = %q, want %q", opts.InboxPrefix, subjects.InboxPrefix("srv_alpha"))
	}
	if opts.MaxReconnect != -1 {
		t.Fatalf("MaxReconnect = %d, want -1 (reconnect forever)", opts.MaxReconnect)
	}
}
