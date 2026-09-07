package agentupdates

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

type fakeStore struct {
	channels map[string]domain.AgentChannelRow
	servers  []domain.Server
	writes   []domain.AgentChannelRow
}

func newFakeStore() *fakeStore {
	return &fakeStore{channels: map[string]domain.AgentChannelRow{
		domain.ChannelStable: {Channel: domain.ChannelStable},
		domain.ChannelCanary: {Channel: domain.ChannelCanary},
	}}
}

func (f *fakeStore) ListAgentChannels(context.Context) ([]domain.AgentChannelRow, error) {
	return []domain.AgentChannelRow{f.channels[domain.ChannelCanary], f.channels[domain.ChannelStable]}, nil
}

func (f *fakeStore) GetAgentChannel(_ context.Context, c string) (domain.AgentChannelRow, error) {
	row, ok := f.channels[c]
	if !ok {
		return domain.AgentChannelRow{}, errors.New("no such channel")
	}
	return row, nil
}

func (f *fakeStore) SetAgentChannel(_ context.Context, c, v, base string, rollback bool, by string) (domain.AgentChannelRow, error) {
	row := domain.AgentChannelRow{Channel: c, DesiredVersion: v, ArtifactBase: base, Rollback: rollback, UpdatedBy: &by}
	f.channels[c] = row
	f.writes = append(f.writes, row)
	return row, nil
}

func (f *fakeStore) SetServerAgentChannel(_ context.Context, id, c string) (domain.Server, error) {
	for i := range f.servers {
		if f.servers[i].ID == id {
			f.servers[i].AgentChannel = c
			return f.servers[i], nil
		}
	}
	return domain.Server{}, errors.New("no such server")
}

func (f *fakeStore) ListServers(context.Context) ([]domain.Server, error) { return f.servers, nil }

func (f *fakeStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	for _, s := range f.servers {
		if s.ID == id {
			return s, nil
		}
	}
	return domain.Server{}, errors.New("no such server")
}

type fakeResync struct{ fleet, servers []string }

func (f *fakeResync) RequestResync(_ context.Context, reason string) error {
	f.fleet = append(f.fleet, reason)
	return nil
}

func (f *fakeResync) RequestServerResync(_ context.Context, id, _ string) error {
	f.servers = append(f.servers, id)
	return nil
}

func newService(t *testing.T, fs *fakeStore, panelVersion string) (*Service, *fakeResync) {
	t.Helper()
	r := &fakeResync{}
	// precheck off: the HEAD is exercised by its own refusal test, and every
	// other test here is about the DECISION rather than about the network.
	return New(fs, r, panelVersion, false, slog.New(slog.NewTextHandler(io.Discard, nil))), r
}

// Additive-only proto guarantees the OLD-agent direction, not the new-agent
// one, so an agent ahead of its plane is a shape nothing tested. The refusal
// names the remedy rather than just saying no.
func TestAVersionNewerThanThePanelIsRefusedNamingTheRemedy(t *testing.T) {
	fs := newFakeStore()
	svc, _ := newService(t, fs, "v1.0.3")

	_, err := svc.Set(context.Background(), domain.ChannelStable, "v1.1.0", "", "ops@example.com")
	if !errors.Is(err, ErrNewerThanPanel) {
		t.Fatalf("err = %v, want ErrNewerThanPanel", err)
	}
	if len(fs.writes) != 0 {
		t.Fatalf("wrote %v despite refusing", fs.writes)
	}

	// A DEVELOPMENT panel cannot make that comparison, and so does not.
	dev, _ := newService(t, newFakeStore(), "dev")
	if _, err := dev.Set(context.Background(), domain.ChannelStable, "v1.1.0", "", "ops@example.com"); err != nil {
		t.Fatalf("a dev panel refused a version it cannot compare: %v", err)
	}
}

// Setting an older version than the channel names permits agents to move
// backwards; moving forward clears it. Derived rather than a second checkbox,
// because an operator who has to remember one is an operator whose rollback
// does not happen.
func TestGoingBackwardsPermitsARollbackAndGoingForwardRevokesIt(t *testing.T) {
	fs := newFakeStore()
	fs.channels[domain.ChannelStable] = domain.AgentChannelRow{Channel: domain.ChannelStable, DesiredVersion: "v1.1.0"}
	svc, _ := newService(t, fs, "v2.0.0")

	row, err := svc.Set(context.Background(), domain.ChannelStable, "v1.0.0", "", "ops@example.com")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !row.Rollback {
		t.Fatal("moving backwards did not permit a rollback")
	}

	row, err = svc.Set(context.Background(), domain.ChannelStable, "v1.2.0", "", "ops@example.com")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if row.Rollback {
		t.Fatal("moving forward left the rollback permission on")
	}
}

// The gate's whole purpose is that a host RAN the candidate. Promoting a
// version nothing has run defeats it, and the refusal says what canary is
// actually running.
func TestPromotionIsRefusedUntilACanaryHostHasActuallyRunTheCandidate(t *testing.T) {
	fs := newFakeStore()
	fs.channels[domain.ChannelCanary] = domain.AgentChannelRow{Channel: domain.ChannelCanary, DesiredVersion: "v1.1.0"}
	fs.servers = []domain.Server{
		{ID: "srv_1", Name: "canary-1", Status: domain.StatusRunning, AgentChannel: domain.ChannelCanary, AgentVersion: "v1.0.3"},
	}
	svc, _ := newService(t, fs, "v1.1.0")

	_, err := svc.Promote(context.Background(), "ops@example.com")
	if !errors.Is(err, ErrGateNoConverged) {
		t.Fatalf("err = %v, want ErrGateNoConverged", err)
	}
	if !strings.Contains(err.Error(), "v1.0.3") {
		t.Fatalf("refusal %q does not say what canary is running", err)
	}

	fs.servers[0].AgentVersion = "v1.1.0"
	row, err := svc.Promote(context.Background(), "ops@example.com")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if row.DesiredVersion != "v1.1.0" || row.Channel != domain.ChannelStable {
		t.Fatalf("promoted to %+v", row)
	}
}

// A canary host that rolled the candidate back blocks promotion by name. There
// is no override flag: an operator who believes a rollback was spurious sets
// stable's version directly and owns that explicitly.
func TestARolledBackCanaryBlocksPromotionByName(t *testing.T) {
	fs := newFakeStore()
	fs.channels[domain.ChannelCanary] = domain.AgentChannelRow{Channel: domain.ChannelCanary, DesiredVersion: "v1.1.0"}
	fs.servers = []domain.Server{
		{ID: "srv_1", Name: "canary-1", Status: domain.StatusRunning, AgentChannel: domain.ChannelCanary, AgentVersion: "v1.1.0"},
		{ID: "srv_2", Name: "canary-2", Status: domain.StatusDegraded, AgentChannel: domain.ChannelCanary,
			AgentVersion: "v1.0.3", AgentUpdatePhase: domain.AgentPhaseRolledBack, AgentUpdateTarget: "v1.1.0"},
	}
	svc, _ := newService(t, fs, "v1.1.0")

	_, err := svc.Promote(context.Background(), "ops@example.com")
	if !errors.Is(err, ErrGateRolledBack) {
		t.Fatalf("err = %v, want ErrGateRolledBack", err)
	}
	if !strings.Contains(err.Error(), "canary-2") {
		t.Fatalf("refusal %q does not name the host", err)
	}
}

// An offline canary neither blocks nor counts. Blocking would make one
// powered-down host a permanent hold on every fleet update, and an operator who
// learns the gate refuses for reasons unrelated to the release is an operator
// who stops reading refusals.
func TestAnOfflineCanaryNeitherBlocksNorCounts(t *testing.T) {
	fs := newFakeStore()
	fs.channels[domain.ChannelCanary] = domain.AgentChannelRow{Channel: domain.ChannelCanary, DesiredVersion: "v1.1.0"}
	fs.servers = []domain.Server{
		{ID: "srv_1", Name: "canary-1", Status: domain.StatusRunning, AgentChannel: domain.ChannelCanary, AgentVersion: "v1.1.0"},
		// Offline, and it rolled the candidate back before it went dark. It
		// must not block: the plane cannot currently verify anything about it.
		{ID: "srv_2", Name: "canary-2", Status: domain.StatusUnknown, AgentChannel: domain.ChannelCanary,
			AgentUpdatePhase: domain.AgentPhaseRolledBack, AgentUpdateTarget: "v1.1.0"},
	}
	svc, _ := newService(t, fs, "v1.1.0")

	if _, err := svc.Promote(context.Background(), "ops@example.com"); err != nil {
		t.Fatalf("an offline canary blocked promotion: %v", err)
	}
}

// The plane must never connect to a host named in a request body. An
// operator-supplied mirror is validated for SHAPE and left alone: a 200 against
// a refusal against a timeout separates a listening port from a closed one from
// a filtered one inside the panel's network (threat-model §5.14), and no
// private-address guard makes "connect to this" not be that — a public host can
// redirect, and a name can resolve differently the second time it is looked up.
//
// The fake client fails every request, so a mirror that reached the network at
// all would surface as ErrArtifactUnreachable rather than succeeding.
func TestAMirrorIsShapeCheckedAndNeverFetchedByThePlane(t *testing.T) {
	fs := newFakeStore()
	svc := New(fs, &fakeResync{}, "v1.1.0", true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.client = &http.Client{Transport: refusingTransport{}}

	row, err := svc.Set(context.Background(), domain.ChannelStable, "v1.1.0", "http://10.0.0.5/releases", "ops@example.com")
	if err != nil {
		t.Fatalf("a private mirror was probed or refused: %v", err)
	}
	if row.ArtifactBase != "http://10.0.0.5/releases" {
		t.Fatalf("artifact base = %q", row.ArtifactBase)
	}

	// Shape is still enforced, because it is the only thing the plane can
	// honestly say about an address it will never connect to.
	for _, bad := range []string{
		"ftp://mirror.example/releases",
		"https://user:pw@mirror.example/releases",
		"https://mirror.example/releases#frag",
		"https://mirror.example/releases?token=abc",
		"https://mirror.example/../../etc",
		"not a url at all",
	} {
		if _, err := svc.Set(context.Background(), domain.ChannelStable, "v1.1.0", bad, "ops@example.com"); !errors.Is(err, ErrBadArtifactBase) {
			t.Fatalf("Set(%q) err = %v, want ErrBadArtifactBase", bad, err)
		}
	}
}

// The version is concatenated into a URL by the plane's pre-flight and by every
// agent's download, so its shape is bounded before anything is stored. A tag of
// "../../" would aim a fetcher at an arbitrary path under the release host —
// the defect core/upgrade.ValidTag already exists to prevent for the panel's
// own releases.
func TestAVersionThatIsNotTagShapedIsRefusedBeforeItIsStored(t *testing.T) {
	fs := newFakeStore()
	svc, _ := newService(t, fs, "v9.9.9")

	for _, bad := range []string{
		"../../../etc/passwd",
		"v1.1.0/../../other",
		"https://evil.example/v1.1.0",
		"v1.1.0 ; rm -rf /",
		"latest",
		"v1.1",
	} {
		if _, err := svc.Set(context.Background(), domain.ChannelStable, bad, "", "ops@example.com"); !errors.Is(err, ErrBadVersion) {
			t.Fatalf("Set(%q) err = %v, want ErrBadVersion", bad, err)
		}
	}
	if len(fs.writes) != 0 {
		t.Fatalf("stored %v despite refusing", fs.writes)
	}

	for _, good := range []string{"v1.1.0", "1.1.0", "v1.1.0-rc.1"} {
		if _, err := svc.Set(context.Background(), domain.ChannelStable, good, "", "ops@example.com"); err != nil {
			t.Fatalf("Set(%q): %v", good, err)
		}
	}
}

// refusingTransport answers nothing, so any test that reaches the network fails
// rather than quietly depending on it.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("the plane must not make this request")
}

// Clearing a channel is how a rollout an operator no longer wants is stopped,
// and it must not be second-guessed by the pre-flight or the ceiling.
func TestClearingAChannelNeedsNoVersionToCheck(t *testing.T) {
	fs := newFakeStore()
	fs.channels[domain.ChannelStable] = domain.AgentChannelRow{Channel: domain.ChannelStable, DesiredVersion: "v9.9.9"}
	svc := New(fs, &fakeResync{}, "v1.0.0", true, slog.New(slog.NewTextHandler(io.Discard, nil)))

	row, err := svc.Set(context.Background(), domain.ChannelStable, "", "", "ops@example.com")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if row.DesiredVersion != "" || row.Rollback {
		t.Fatalf("cleared channel = %+v", row)
	}
}

// Moving one server between channels nudges ONLY that server: a channel change
// is one host's business, and waking the fleet for it would make every dropdown
// a fleet-wide event.
func TestMovingOneServerNudgesOnlyThatServer(t *testing.T) {
	fs := newFakeStore()
	fs.servers = []domain.Server{{ID: "srv_1", Name: "web-1"}}
	svc, r := newService(t, fs, "v1.0.0")

	if _, err := svc.SetServerChannel(context.Background(), "srv_1", domain.ChannelCanary); err != nil {
		t.Fatalf("SetServerChannel: %v", err)
	}
	if len(r.fleet) != 0 {
		t.Fatalf("nudged the fleet: %v", r.fleet)
	}
	if len(r.servers) != 1 || r.servers[0] != "srv_1" {
		t.Fatalf("server nudges = %v", r.servers)
	}
}
