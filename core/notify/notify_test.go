package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// identityOpener returns the ciphertext unchanged, so tests store raw config
// JSON directly in ConfigCT.
type identityOpener struct{}

func (identityOpener) Open(ct, _ []byte) ([]byte, error) { return ct, nil }

// mgrStore fakes the manager's Store: it resolves an env to a project and
// filters notifiers by (project, event, enabled).
type mgrStore struct {
	env       domain.Environment
	notifiers []domain.Notifier
}

func (s *mgrStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	return s.env, nil
}

// GetProject answers with a name that differs from the environment's, so a
// test can tell which one landed in NotifyEvent.Project.
func (s *mgrStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	return domain.Project{ID: id, Name: "shop"}, nil
}
func (s *mgrStore) ListEnabledNotifiersForEvent(_ context.Context, projectID, eventType string) ([]domain.Notifier, error) {
	var out []domain.Notifier
	for _, n := range s.notifiers {
		if n.ProjectID == projectID && n.Enabled && contains(n.Events, eventType) {
			out = append(out, n)
		}
	}
	return out, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func webhookNotifier(id, channel, url string, events ...string) domain.Notifier {
	return domain.Notifier{
		ID: id, ProjectID: "prj_1", Channel: channel, Enabled: true, Events: events,
		ConfigCT: []byte(`{"webhook_url":"` + url + `"}`),
	}
}

// --- synchronous delivery (Deliver) ---

func TestDeliverSlackPostsText(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	m := New(&mgrStore{}, identityOpener{}, quietLog(), nil)
	n := webhookNotifier("ntf_1", domain.NotifyChannelSlack, srv.URL, domain.EventDeployFailed)
	ev := domain.NotifyEvent{Type: domain.EventDeployFailed, Title: "T", Body: "B"}
	if err := m.Deliver(context.Background(), n, ev); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got["text"] != "T\nB" {
		t.Fatalf("slack payload text = %q, want %q", got["text"], "T\nB")
	}
}

func TestDeliverDiscordPostsContent(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	m := New(&mgrStore{}, identityOpener{}, quietLog(), nil)
	n := webhookNotifier("ntf_1", domain.NotifyChannelDiscord, srv.URL, domain.EventDeployFailed)
	if err := m.Deliver(context.Background(), n, domain.NotifyEvent{Title: "T", Body: "B"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got["content"] != "T\nB" {
		t.Fatalf("discord payload content = %q", got["content"])
	}
}

func TestDeliverTelegramUsesTokenPath(t *testing.T) {
	var path string
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	old := telegramAPI
	telegramAPI = srv.URL
	defer func() { telegramAPI = old }()

	m := New(&mgrStore{}, identityOpener{}, quietLog(), nil)
	n := domain.Notifier{
		ID: "ntf_1", Channel: domain.NotifyChannelTelegram,
		ConfigCT: []byte(`{"bot_token":"TOK","chat_id":"42"}`),
	}
	if err := m.Deliver(context.Background(), n, domain.NotifyEvent{Title: "T", Body: "B"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !strings.Contains(path, "/botTOK/sendMessage") {
		t.Fatalf("telegram path = %q, want token in path", path)
	}
	if got["chat_id"] != "42" || got["text"] != "T\nB" {
		t.Fatalf("telegram payload = %v", got)
	}
}

func TestDeliverNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := New(&mgrStore{}, identityOpener{}, quietLog(), nil)
	n := webhookNotifier("ntf_1", domain.NotifyChannelSlack, srv.URL, domain.EventDeployFailed)
	if err := m.Deliver(context.Background(), n, domain.NotifyEvent{}); err == nil {
		t.Fatal("non-2xx response should be a delivery error")
	}
}

// --- async dispatch (NotifyDeploy) ---

func TestNotifyDeployReachesOnlySubscribed(t *testing.T) {
	var failHits int32
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&failHits, 1) }))
	defer fail.Close()

	hit := make(chan struct{}, 1)
	succWatched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
	}))
	defer succWatched.Close()

	st := &mgrStore{
		env: domain.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production"},
		notifiers: []domain.Notifier{
			webhookNotifier("ntf_succ", domain.NotifyChannelSlack, succWatched.URL, domain.EventDeploySucceeded),
			webhookNotifier("ntf_fail", domain.NotifyChannelSlack, fail.URL, domain.EventDeployFailed),
		},
	}
	m := New(st, identityOpener{}, quietLog(), nil)

	// A successful deploy must reach only the deploy.succeeded subscriber.
	m.NotifyDeploy(context.Background(), domain.Application{Name: "web", EnvironmentID: "env_1"},
		domain.Deployment{ID: "dep_1", Status: domain.DeploySucceeded, RevisionID: "rev_1"})

	select {
	case <-hit:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribed notifier was not delivered to")
	}
	if got := atomic.LoadInt32(&failHits); got != 0 {
		t.Fatalf("unsubscribed notifier received %d deliveries, want 0", got)
	}
}

func TestFanOutIsolatesAFailingChannel(t *testing.T) {
	hit := make(chan struct{}, 1)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- struct{}{} }))
	defer good.Close()

	st := &mgrStore{
		env: domain.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production"},
		notifiers: []domain.Notifier{
			// An unreachable endpoint (nothing listening on this port).
			webhookNotifier("ntf_bad", domain.NotifyChannelSlack, "http://127.0.0.1:1/hook", domain.EventDeployFailed),
			webhookNotifier("ntf_good", domain.NotifyChannelSlack, good.URL, domain.EventDeployFailed),
		},
	}
	m := New(st, identityOpener{}, quietLog(), nil)
	m.NotifyDeploy(context.Background(), domain.Application{Name: "web", EnvironmentID: "env_1"},
		domain.Deployment{ID: "dep_1", Status: domain.DeployFailed, Detail: "boom"})

	select {
	case <-hit:
	case <-time.After(2 * time.Second):
		t.Fatal("a failing sibling channel blocked the healthy one")
	}
}

func TestNotifyDeployDetachedFromCanceledContext(t *testing.T) {
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- struct{}{} }))
	defer srv.Close()

	st := &mgrStore{
		env:       domain.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production"},
		notifiers: []domain.Notifier{webhookNotifier("ntf_1", domain.NotifyChannelSlack, srv.URL, domain.EventDeploySucceeded)},
	}
	m := New(st, identityOpener{}, quietLog(), nil)

	// The caller's context is canceled the instant NotifyDeploy returns (the
	// scheduler's handler context). Delivery must still complete (spec §1).
	ctx, cancel := context.WithCancel(context.Background())
	m.NotifyDeploy(ctx, domain.Application{Name: "web", EnvironmentID: "env_1"},
		domain.Deployment{ID: "dep_1", Status: domain.DeploySucceeded})
	cancel()

	select {
	case <-hit:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery was canceled with the caller's context")
	}
}

// --- the inbox audience (notification-inbox.md §1, §4) ---

// recordingInbox captures what dispatch hands the inbox, and signals when it
// has, so the detached goroutine can be awaited rather than slept on.
type recordingInbox struct {
	got  chan domain.NotifyEvent
	fail error
}

func newRecordingInbox() *recordingInbox {
	return &recordingInbox{got: make(chan domain.NotifyEvent, 4)}
}

func (r *recordingInbox) Record(_ context.Context, ev domain.NotifyEvent) error {
	r.got <- ev
	return r.fail
}

// The whole point of the inbox: a panel with NO notifiers configured at all —
// which is most panels — still gets a record of what happened. The inbox write
// comes before the notifier lookup precisely so this holds.
func TestDispatchRecordsToTheInboxWithZeroNotifiers(t *testing.T) {
	box := newRecordingInbox()
	st := &mgrStore{env: domain.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production"}}
	m := New(st, identityOpener{}, quietLog(), box)

	m.NotifyDeploy(context.Background(),
		domain.Application{ID: "app_web", Name: "web", EnvironmentID: "env_1"},
		domain.Deployment{ID: "dep_9", Status: domain.DeployFailed, RevisionID: "rev_1", Detail: "healthcheck never passed"})

	select {
	case ev := <-box.got:
		if ev.Type != domain.EventDeployFailed || ev.Level != domain.NotifyError {
			t.Fatalf("event = %s/%s, want deploy.failed/error", ev.Type, ev.Level)
		}
		if ev.ProjectID != "prj_1" {
			t.Fatalf("project_id = %q, want the resolved project", ev.ProjectID)
		}
		// The project's NAME, not the environment's: "production" is where it
		// happened, "shop" is whose it is (control-plane-hardening.md §8).
		if ev.Project != "shop" {
			t.Fatalf("project = %q, want the project name \"shop\", not the environment name", ev.Project)
		}
		// The four additive fields are what let the item carry a deep link.
		if ev.ResourceKind != domain.WebhookResourceApplication || ev.ResourceID != "app_web" || ev.FocusID != "dep_9" {
			t.Fatalf("link fields = %q/%q/%q", ev.ResourceKind, ev.ResourceID, ev.FocusID)
		}
		// The bell and a Slack message render the same value, so the words match.
		if ev.Title != "Deploy failed: web" {
			t.Fatalf("title = %q", ev.Title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbox record with zero notifiers configured")
	}
}

func TestDispatchRecordsBackupsWithTheirRecord(t *testing.T) {
	box := newRecordingInbox()
	st := &mgrStore{env: domain.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production"}}
	m := New(st, identityOpener{}, quietLog(), box)

	m.NotifyBackup(context.Background(),
		domain.Database{ID: "db_atlas", Name: "atlas-pg", EnvironmentID: "env_1"},
		domain.BackupRecord{ID: "br_3", Status: domain.BackupSucceeded})

	select {
	case ev := <-box.got:
		if ev.Type != domain.EventBackupSucceeded || ev.ResourceKind != domain.WebhookResourceDatabase {
			t.Fatalf("event = %s/%s", ev.Type, ev.ResourceKind)
		}
		if ev.ResourceID != "db_atlas" || ev.FocusID != "br_3" {
			t.Fatalf("link fields = %q/%q", ev.ResourceID, ev.FocusID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbox record for a backup outcome")
	}
}

// A failing inbox write is logged, never fatal to the channel fan-out: the two
// audiences are independent, and a dead database must not silence Slack.
func TestChannelDeliveryContinuesWhenTheInboxFails(t *testing.T) {
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- struct{}{} }))
	defer srv.Close()

	box := newRecordingInbox()
	box.fail = errors.New("database is down")
	st := &mgrStore{
		env:       domain.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production"},
		notifiers: []domain.Notifier{webhookNotifier("ntf_1", domain.NotifyChannelSlack, srv.URL, domain.EventDeployFailed)},
	}
	m := New(st, identityOpener{}, quietLog(), box)
	m.NotifyDeploy(context.Background(),
		domain.Application{ID: "app_web", Name: "web", EnvironmentID: "env_1"},
		domain.Deployment{ID: "dep_1", Status: domain.DeployFailed})

	select {
	case <-hit:
	case <-time.After(2 * time.Second):
		t.Fatal("a failing inbox write stopped the channel fan-out")
	}
}

// Every header value is neutralised, not just the subject. A recipient carrying
// a line break would otherwise end the To header and start another one — which
// is how one address becomes a Bcc to somewhere else (CWE-640).
func TestBuildMessageNeutralisesEveryHeader(t *testing.T) {
	msg := string(buildMessage(
		"from@example.com\r\nBcc: sneaky-from@evil.test",
		[]string{"to@example.com\r\nBcc: sneaky-to@evil.test"},
		"subject\r\nBcc: sneaky-subject@evil.test",
		"body line one\nbody line two",
	))
	headers, body, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatal("message has no header/body separator")
	}
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("a header was injected: %q", line)
		}
	}
	if !strings.Contains(body, "body line one\r\nbody line two") {
		t.Fatalf("body newlines were not normalised: %q", body)
	}
}

// A body is assembled from commit messages, container log lines and operator
// notes, so it can carry any line ending at all. Every one must collapse to
// exactly one CRLF: a lone CR inside DATA is an SMTP protocol violation, and
// the obvious ReplaceAll("\n", "\r\n") leaves it in place (CodeQL
// go/email-injection on the DATA sink).
func TestNormalizeBodyCollapsesEveryLineEnding(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"lone LF":            {"a\nb", "a\r\nb"},
		"lone CR":            {"a\rb", "a\r\nb"},
		"CRLF stays one":     {"a\r\nb", "a\r\nb"},
		"mixed":              {"a\r\nb\nc\rd", "a\r\nb\r\nc\r\nd"},
		"trailing CR":        {"a\r", "a\r\n"},
		"consecutive breaks": {"a\n\nb", "a\r\n\r\nb"},
		"CR CR":              {"a\r\rb", "a\r\n\r\nb"},
		"no breaks":          {"plain text", "plain text"},
		"empty":              {"", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := normalizeBody(c.in); got != c.want {
				t.Fatalf("normalizeBody(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The body cannot introduce a header no matter what it contains: it sits after
// the blank line, so a header-shaped line in it is body text.
func TestBuildMessageBodyCannotInjectAHeader(t *testing.T) {
	msg := string(buildMessage(
		"from@example.com",
		[]string{"to@example.com"},
		"deploy failed",
		"log said:\r\nBcc: sneaky-body@evil.test\rX-Injected: yes",
	))
	headers, body, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatal("message has no header/body separator")
	}
	for _, line := range strings.Split(headers, "\r\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "bcc:") || strings.HasPrefix(lower, "x-injected:") {
			t.Fatalf("the body reached the header block: %q", line)
		}
	}
	// It is still delivered — as text the recipient can read, on its own line.
	if !strings.Contains(body, "\r\nBcc: sneaky-body@evil.test\r\nX-Injected: yes") {
		t.Fatalf("body content was mangled: %q", body)
	}
	// And no bare CR survives anywhere in the message.
	for i := 0; i < len(msg)-1; i++ {
		if msg[i] == '\r' && msg[i+1] != '\n' {
			t.Fatalf("a bare CR survived at offset %d: %q", i, msg)
		}
	}
}

// ─── egress guard on the unsaved-config test path (threat-model §5.11) ───────

// The guard is what keeps "test this config" from being a synchronous port
// scanner with no trace. Everything an operator's own infrastructure answers on
// is refused; a public address is not.
func TestPubliclyRoutable(t *testing.T) {
	refused := []string{
		"127.0.0.1", "::1", // loopback
		"10.0.0.7", "172.16.0.1", "192.168.1.1", // RFC1918
		"fd00::1",                    // IPv6 unique-local
		"169.254.169.254", "fe80::1", // link-local: cloud metadata lives here
		"0.0.0.0", "::", // unspecified
		"224.0.0.1", "ff02::1", // multicast
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"::ffff:10.0.0.7",  // IPv4-mapped RFC1918
	}
	for _, s := range refused {
		if publiclyRoutable(net.ParseIP(s)) {
			t.Fatalf("publiclyRoutable(%s) = true, want false", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		if !publiclyRoutable(net.ParseIP(s)) {
			t.Fatalf("publiclyRoutable(%s) = false, want true", s)
		}
	}
	// An unparseable address is refused rather than assumed safe.
	if publiclyRoutable(nil) {
		t.Fatal("publiclyRoutable(nil) = true, want false")
	}
}

// The guard runs in the dialer's Control hook, so it sees the resolved address
// the socket is about to use — which is what makes it proof against a name that
// resolves publicly once and privately the next time.
func TestGuardControlRefusesPrivateAddresses(t *testing.T) {
	if err := guardControl("tcp", "127.0.0.1:8080", nil); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("guardControl(loopback) = %v, want ErrPrivateDestination", err)
	}
	if err := guardControl("tcp", "169.254.169.254:80", nil); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("guardControl(metadata) = %v, want ErrPrivateDestination", err)
	}
	if err := guardControl("tcp", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("guardControl(public) = %v, want nil", err)
	}
	// A malformed address is refused, not passed through.
	if err := guardControl("tcp", "not-an-address", nil); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("guardControl(malformed) = %v, want ErrPrivateDestination", err)
	}
}

// The unsaved-config test refuses what a saved notifier is allowed to keep
// doing. Both layers are checked: the URL policy, which runs before anything is
// dialled, and the dial-time guard for a name that only resolves privately.
func TestTestConfigRefusesUnsafeWebhookTargets(t *testing.T) {
	m := New(nil, nil, quietLog(), nil)
	ctx := context.Background()

	for name, raw := range map[string]string{
		"cleartext":      `{"webhook_url":"http://hooks.example.com/x"}`,
		"ip literal":     `{"webhook_url":"https://93.184.216.34/x"}`,
		"loopback":       `{"webhook_url":"https://127.0.0.1:9/x"}`,
		"no dot in host": `{"webhook_url":"https://intranet/x"}`,
		"userinfo":       `{"webhook_url":"https://user:pw@hooks.example.com/x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := m.TestConfig(ctx, domain.NotifyChannelSlack, json.RawMessage(raw))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("TestConfig(%s) = %v, want a ValidationError", raw, err)
			}
		})
	}
}

// A saved notifier keeps the documented posture and may still point at an
// internal host — which is what makes a self-hosted receiver work. The
// asymmetry between the two paths is the point.
func TestSavedNotifierStillDeliversToTheLocalNetwork(t *testing.T) {
	var got atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := New(nil, nil, quietLog(), nil)
	cfg := json.RawMessage(`{"webhook_url":"` + srv.URL + `"}`)
	if err := m.send(context.Background(), domain.NotifyChannelSlack, cfg, TestEvent()); err != nil {
		t.Fatalf("saved-notifier delivery to a loopback address failed: %v", err)
	}
	if got.Load() != 1 {
		t.Fatalf("receiver saw %d requests, want 1", got.Load())
	}

	// The same address through the unsaved path is refused.
	if err := m.TestConfig(context.Background(), domain.NotifyChannelSlack, cfg); err == nil {
		t.Fatal("the unsaved test reached a loopback address")
	}
}

// Email has no unsaved test: it would relay a message through an arbitrary
// server, with an arbitrary From, to an arbitrary recipient, leaving no record.
func TestTestConfigRefusesUnsavedEmail(t *testing.T) {
	m := New(nil, nil, quietLog(), nil)
	raw := json.RawMessage(`{"smtp_host":"smtp.example.test","smtp_port":587,"from":"a@example.test","to":"b@example.test"}`)
	if err := m.TestConfig(context.Background(), domain.NotifyChannelEmail, raw); !errors.Is(err, ErrTestRequiresSave) {
		t.Fatalf("TestConfig(email) = %v, want ErrTestRequiresSave", err)
	}
}

// A well-formed provider URL is accepted by the policy — the check must not be
// so strict that the product stops working.
func TestTestableWebhookURLAcceptsProviderEndpoints(t *testing.T) {
	for _, u := range []string{
		"https://hooks.slack.com/services/T00/B00/XXXX",
		"https://discord.com/api/webhooks/123/abc",
		"https://hooks.example.co.uk:8443/path?x=1",
	} {
		if !testableWebhookURL.MatchString(u) {
			t.Fatalf("testableWebhookURL rejected a real endpoint: %s", u)
		}
	}
}

// An address that cannot be parsed cannot be stored, so a line break can never
// reach a header in the first place.
func TestEmailConfigRejectsUnparseableAddresses(t *testing.T) {
	for _, cfg := range []string{
		`{"smtp_host":"smtp.test","smtp_port":587,"from":"ops@test\nBcc: elsewhere@evil.test","to":"a@test"}`,
		`{"smtp_host":"smtp.test","smtp_port":587,"from":"ops@test","to":"not an address"}`,
		`{"smtp_host":"smtp.test","smtp_port":587,"from":"not an address","to":"a@test"}`,
	} {
		if _, err := validateConfig(domain.NotifyChannelEmail, json.RawMessage(cfg)); err == nil {
			t.Fatalf("validateConfig accepted %s", cfg)
		}
	}
	ok := `{"smtp_host":"smtp.test","smtp_port":587,"from":"ops@test.example","to":"a@test.example, b@test.example"}`
	if _, err := validateConfig(domain.NotifyChannelEmail, json.RawMessage(ok)); err != nil {
		t.Fatalf("validateConfig refused a valid config: %v", err)
	}
}
