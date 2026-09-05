package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
