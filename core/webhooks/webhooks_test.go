package webhooks

// Manager tests (outbound-webhooks.md §4, §5, §8). Every attempt goes through a
// real httptest receiver, so the signature, the headers and the retry decision
// are exercised as a receiver would see them.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// clock is a manually advanced time source, so backoff windows are asserted
// rather than waited for (ENGINEERING rule 9).
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: fixedNow} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// receiver is a scripted webhook target: it answers the next status in its
// script and records what it was sent.
type receiver struct {
	mu       sync.Mutex
	script   []int
	requests []capturedRequest
	srv      *httptest.Server
	gotOne   chan struct{}
	closed   bool
}

type capturedRequest struct {
	Body      []byte
	Event     string
	Delivery  string
	Timestamp string
	Signature string
}

func newReceiver(t *testing.T, script ...int) *receiver {
	t.Helper()
	r := &receiver{script: script, gotOne: make(chan struct{}, 16)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, capturedRequest{
			Body:      body,
			Event:     req.Header.Get(HeaderEvent),
			Delivery:  req.Header.Get(HeaderDelivery),
			Timestamp: req.Header.Get(HeaderTimestamp),
			Signature: req.Header.Get(HeaderSignature),
		})
		status := http.StatusOK
		if n := len(r.requests) - 1; n < len(r.script) {
			status = r.script[n]
		} else if len(r.script) > 0 {
			status = r.script[len(r.script)-1]
		}
		r.mu.Unlock()
		select {
		case r.gotOne <- struct{}{}:
		default:
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.closed {
			r.closed = true
			r.srv.Close()
		}
	})
	return r
}

func (r *receiver) url() string { return r.srv.URL + "/hooks/cypher" }

func (r *receiver) captured() []capturedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// waitFor blocks until the receiver has taken n requests, or fails the test.
func (r *receiver) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if len(r.captured()) >= n {
			return
		}
		select {
		case <-r.gotOne:
		case <-deadline:
			t.Fatalf("receiver took %d requests, want %d", len(r.captured()), n)
		}
	}
}

func newTestManager(st *fakeStore, c *clock) *Manager {
	m := New(st, identitySealer{}, quietLog())
	m.now = c.now
	// A fixed midpoint jitter keeps the backoff exactly at its nominal delay.
	m.jitter = func() float64 { return 0.5 }
	return m
}

// verify reproduces the receiver-side check documented in openapi.yaml.
func verify(t *testing.T, secret string, got capturedRequest) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(got.Timestamp))
	mac.Write([]byte("."))
	mac.Write(got.Body)
	want := signaturePrefix + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(got.Signature)) {
		t.Fatalf("signature %q does not verify (want %q)", got.Signature, want)
	}
	if _, err := strconv.ParseInt(got.Timestamp, 10, 64); err != nil {
		t.Fatalf("timestamp header %q is not unix seconds", got.Timestamp)
	}
}

// Acceptance 1: a passing deploy reaches the receiver exactly once, the
// signature verifies against the create secret, and the delivery is logged
// succeeded with a status and a duration.
func TestDeployEventDeliversSignedAndLogged(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusOK)
	const secret = "s3cret-signing-key"
	st.seedEndpoint("whe_1", "prj_1", rec.url(), secret, []string{domain.EventDeploySucceeded}, true)

	m.NotifyDeploy(context.Background(), domain.Application{ID: "app_1", EnvironmentID: "env_1", Name: "web"},
		domain.Deployment{ID: "dep_1", RevisionID: "rev_1", Status: domain.DeploySucceeded, Trigger: "webhook"})
	rec.waitFor(t, 1)

	got := rec.captured()[0]
	verify(t, secret, got)
	if got.Event != domain.EventDeploySucceeded {
		t.Fatalf("event header = %q", got.Event)
	}
	if !strings.HasPrefix(got.Delivery, "whd_") {
		t.Fatalf("delivery header = %q, want a whd_ id", got.Delivery)
	}

	var env envelope
	if err := json.Unmarshal(got.Body, &env); err != nil {
		t.Fatalf("payload is not the documented envelope: %v", err)
	}
	if env.DeliveryID != got.Delivery {
		t.Fatalf("payload delivery_id %q != header %q", env.DeliveryID, got.Delivery)
	}
	// Both names are resolved — notify.dispatch assigns the environment name to
	// the project field, and this slice does not inherit that (spec §4).
	if env.Project.Name != "atlas-crm" || env.Environment.Name != "production" {
		t.Fatalf("project/environment = %q/%q, want atlas-crm/production", env.Project.Name, env.Environment.Name)
	}
	if env.Resource.Kind != domain.WebhookResourceApplication || env.Resource.Name != "web" {
		t.Fatalf("resource = %+v", env.Resource)
	}
	if env.Data.DeploymentID != "dep_1" || env.Data.RevisionID != "rev_1" {
		t.Fatalf("data = %+v", env.Data)
	}

	waitForStatus(t, st, got.Delivery, domain.DeliverySucceeded)
	attempts := st.attemptsFor(got.Delivery)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].ResponseStatus == nil || *attempts[0].ResponseStatus != http.StatusOK {
		t.Fatalf("attempt status = %v, want 200", attempts[0].ResponseStatus)
	}
	if attempts[0].Error != "" {
		t.Fatalf("attempt error = %q, want empty on success", attempts[0].Error)
	}
	// Retention is pruned on insert (spec §6).
	if len(st.pruned) == 0 || st.pruned[0].Keep != deliveryRetention {
		t.Fatalf("prune calls = %+v, want one keeping %d", st.pruned, deliveryRetention)
	}
}

// Acceptance 2: 500, 500, 200 → one delivery, three attempts, final succeeded,
// with backoff timings inside the documented windows.
func TestRetryUntilSuccessRecordsEveryAttempt(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusInternalServerError, http.StatusInternalServerError, http.StatusOK)
	e := st.seedEndpoint("whe_2", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	d := enqueueForTest(t, m, e)
	d = attempt(t, m, e, d)
	if d.Status != domain.DeliveryPending || d.Attempt != 1 {
		t.Fatalf("after attempt 1: status=%s attempt=%d, want pending/1", d.Status, d.Attempt)
	}
	assertNextIn(t, d, c.now(), backoff[0])

	c.advance(backoff[0])
	d = attempt(t, m, e, d)
	if d.Status != domain.DeliveryPending || d.Attempt != 2 {
		t.Fatalf("after attempt 2: status=%s attempt=%d", d.Status, d.Attempt)
	}
	assertNextIn(t, d, c.now(), backoff[1])

	c.advance(backoff[1])
	d = attempt(t, m, e, d)
	if d.Status != domain.DeliverySucceeded {
		t.Fatalf("after attempt 3: status=%s, want succeeded", d.Status)
	}
	if d.NextAttemptAt != nil {
		t.Fatal("a terminal delivery must not keep a next_attempt_at")
	}

	attempts := st.attemptsFor(d.ID)
	if len(attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", len(attempts))
	}
	for i, a := range attempts {
		if a.Attempt != i+1 {
			t.Fatalf("attempt %d numbered %d", i, a.Attempt)
		}
	}
	// The delivery id is stable across retries — a receiver dedupes on it.
	got := rec.captured()
	if got[0].Delivery != got[2].Delivery {
		t.Fatalf("delivery header changed across retries: %q vs %q", got[0].Delivery, got[2].Delivery)
	}
}

// Acceptance 2 (second half): a 404 is terminal — the receiver answered, and it
// will answer the same in five minutes.
func TestClientErrorIsTerminal(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusNotFound)
	e := st.seedEndpoint("whe_3", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	d := attempt(t, m, e, enqueueForTest(t, m, e))
	if d.Status != domain.DeliveryFailed {
		t.Fatalf("status = %s, want failed", d.Status)
	}
	if d.NextAttemptAt != nil {
		t.Fatal("a 404 scheduled a retry; it must be terminal")
	}
	if n := len(st.attemptsFor(d.ID)); n != 1 {
		t.Fatalf("attempts = %d, want exactly 1", n)
	}
}

// 429 and 5xx are retryable; a 3xx is not (redirects are never followed, so a
// receiver cannot bounce a signed body elsewhere — spec §6).
func TestRetryClassification(t *testing.T) {
	cases := map[string]struct {
		status int
		want   bool
	}{
		"200 succeeded":   {http.StatusOK, false},
		"301 redirect":    {http.StatusMovedPermanently, false},
		"401 unauthed":    {http.StatusUnauthorized, false},
		"404 gone":        {http.StatusNotFound, false},
		"429 throttled":   {http.StatusTooManyRequests, true},
		"500 server":      {http.StatusInternalServerError, true},
		"503 unavailable": {http.StatusServiceUnavailable, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := retryable(tc.status, nil); got != tc.want {
				t.Fatalf("retryable(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
	if !retryable(0, io.ErrUnexpectedEOF) {
		t.Fatal("a transport error must be retryable")
	}
}

// Acceptance 3: 500 forever → exactly four attempts, terminal failed, and the
// endpoint reads "failing".
func TestExhaustedRetriesEndFailing(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusInternalServerError)
	e := st.seedEndpoint("whe_4", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	d := enqueueForTest(t, m, e)
	for i := 0; i < maxAttempts; i++ {
		d = attempt(t, m, e, d)
		if i < maxAttempts-1 {
			c.advance(backoff[min(i, len(backoff)-1)])
		}
	}
	if d.Status != domain.DeliveryFailed || d.Attempt != maxAttempts {
		t.Fatalf("status=%s attempt=%d, want failed/%d", d.Status, d.Attempt, maxAttempts)
	}
	if n := len(st.attemptsFor(d.ID)); n != maxAttempts {
		t.Fatalf("attempts = %d, want %d", n, maxAttempts)
	}
	if n := len(rec.captured()); n != maxAttempts {
		t.Fatalf("receiver saw %d requests, want %d", n, maxAttempts)
	}

	svc := NewService(st, identitySealer{}, m)
	v, err := svc.View(context.Background(), e.ID)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Health != domain.EndpointFailing {
		t.Fatalf("health = %q, want failing", v.Health)
	}
}

// Acceptance 4: redelivering creates a new row with a fresh id and
// redelivery_of set, leaves the original untouched, and the receiver sees a
// different X-CypherPanel-Delivery.
func TestRedeliverMintsNewDeliveryAndLeavesTheOriginal(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusOK)
	e := st.seedEndpoint("whe_5", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	orig := attempt(t, m, e, enqueueForTest(t, m, e))
	rec.waitFor(t, 1)

	replay, err := m.Redeliver(context.Background(), e, orig)
	if err != nil {
		t.Fatalf("Redeliver: %v", err)
	}
	rec.waitFor(t, 2)

	if replay.ID == orig.ID {
		t.Fatal("redelivery reused the original id")
	}
	if replay.RedeliveryOf == nil || *replay.RedeliveryOf != orig.ID {
		t.Fatalf("redelivery_of = %v, want %s", replay.RedeliveryOf, orig.ID)
	}
	if replay.Payload != orig.Payload {
		t.Fatal("redelivery did not replay the stored payload bytes verbatim")
	}
	// The original row is evidence: never mutated by a replay.
	after, err := st.GetWebhookDelivery(context.Background(), orig.ID)
	if err != nil {
		t.Fatalf("GetWebhookDelivery: %v", err)
	}
	if after.Status != orig.Status || after.Attempt != orig.Attempt || after.RedeliveryOf != nil {
		t.Fatalf("original mutated by the replay: %+v", after)
	}

	got := rec.captured()
	if got[0].Delivery == got[1].Delivery {
		t.Fatal("the receiver saw the same delivery id twice; a replay must be distinguishable")
	}
	if string(got[0].Body) != string(got[1].Body) {
		t.Fatal("a replay must send the same body bytes")
	}
}

// Acceptance 5 + 7: a disabled endpoint receives nothing, and an event in one
// project never reaches another project's endpoints.
func TestFanOutRespectsEnabledAndProjectBoundary(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)

	wanted := newReceiver(t, http.StatusOK)
	off := newReceiver(t, http.StatusOK)
	foreign := newReceiver(t, http.StatusOK)
	unsubscribed := newReceiver(t, http.StatusOK)

	st.seedEndpoint("whe_a", "prj_1", wanted.url(), "s", []string{domain.EventDeployFailed}, true)
	st.seedEndpoint("whe_b", "prj_1", off.url(), "s", []string{domain.EventDeployFailed}, false)
	st.seedEndpoint("whe_c", "prj_1", unsubscribed.url(), "s", []string{domain.EventBackupFailed}, true)
	st.seedEndpoint("whe_d", "prj_2", foreign.url(), "s", []string{domain.EventDeployFailed}, true)

	m.NotifyDeploy(context.Background(), domain.Application{ID: "app_1", EnvironmentID: "env_1", Name: "web"},
		domain.Deployment{ID: "dep_1", RevisionID: "rev_1", Status: domain.DeployFailed, Detail: "health check never passed"})
	wanted.waitFor(t, 1)

	// Give any wrongly-addressed delivery a chance to land before asserting.
	time.Sleep(150 * time.Millisecond)
	for name, r := range map[string]*receiver{"disabled": off, "unsubscribed": unsubscribed, "other project": foreign} {
		if n := len(r.captured()); n != 0 {
			t.Fatalf("%s endpoint received %d deliveries, want 0", name, n)
		}
	}
	if n := len(wanted.captured()); n != 1 {
		t.Fatalf("subscribed endpoint received %d deliveries, want 1", n)
	}
}

// A dead receiver must not slow or fail the caller: NotifyDeploy returns
// immediately, long before the 10s per-attempt timeout could elapse (spec §5).
func TestNotifyDeployNeverBlocksTheCaller(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)

	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); hung.Close() }()
	st.seedEndpoint("whe_slow", "prj_1", hung.URL, "s", []string{domain.EventDeploySucceeded}, true)

	done := make(chan struct{})
	go func() {
		m.NotifyDeploy(context.Background(), domain.Application{ID: "app_1", EnvironmentID: "env_1", Name: "web"},
			domain.Deployment{ID: "dep_1", RevisionID: "rev_1", Status: domain.DeploySucceeded})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyDeploy blocked on a hung receiver")
	}
}

// Acceptance 7: a delivery whose next_attempt_at has passed is attempted by the
// sweeper — the restart-safety property, since the row and its schedule are in
// Postgres before the first attempt (ENGINEERING rule 15).
func TestSweeperAttemptsDueDeliveries(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusInternalServerError, http.StatusOK)
	e := st.seedEndpoint("whe_6", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	d := attempt(t, m, e, enqueueForTest(t, m, e))
	if d.Status != domain.DeliveryPending {
		t.Fatalf("status = %s, want pending after a 500", d.Status)
	}

	// Not due yet: the sweep is a no-op.
	m.SweepDue(context.Background())
	if n := len(rec.captured()); n != 1 {
		t.Fatalf("sweep before the backoff elapsed made %d requests, want 1 total", n)
	}

	c.advance(backoff[0])
	m.SweepDue(context.Background())
	waitForStatus(t, st, d.ID, domain.DeliverySucceeded)
	if n := len(rec.captured()); n != 2 {
		t.Fatalf("receiver saw %d requests, want 2", n)
	}
}

// Switching an endpoint off mid-backoff abandons its pending deliveries without
// making a request — so no attempt row is written for a request never made.
func TestSweeperAbandonsDeliveriesForDisabledEndpoints(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusInternalServerError)
	e := st.seedEndpoint("whe_7", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	d := attempt(t, m, e, enqueueForTest(t, m, e))
	e.Enabled = false
	if _, err := st.UpdateWebhookEndpoint(context.Background(), e); err != nil {
		t.Fatalf("UpdateWebhookEndpoint: %v", err)
	}

	c.advance(backoff[0])
	m.SweepDue(context.Background())

	after, err := st.GetWebhookDelivery(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("GetWebhookDelivery: %v", err)
	}
	if after.Status != domain.DeliveryFailed || after.NextAttemptAt != nil {
		t.Fatalf("delivery = %s / next=%v, want terminal failed", after.Status, after.NextAttemptAt)
	}
	if n := len(rec.captured()); n != 1 {
		t.Fatalf("receiver saw %d requests, want only the pre-disable one", n)
	}
	if n := len(st.attemptsFor(d.ID)); n != 1 {
		t.Fatalf("attempts = %d — no request was made, so none should be recorded", n)
	}
}

// A transport error is stored redacted: the URL can carry an operator's token
// in its query string, so it must not ride out inside a *url.Error (spec §6).
func TestTransportErrorIsRedacted(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	dead := newReceiver(t, http.StatusOK)
	target := dead.url() + "?token=super-secret-token"
	e := st.seedEndpoint("whe_8", "prj_1", target, "s", []string{domain.EventDeployFailed}, true)
	dead.mu.Lock()
	dead.closed = true
	dead.mu.Unlock()
	dead.srv.Close() // nothing is listening now

	d := attempt(t, m, e, enqueueForTest(t, m, e))
	attempts := st.attemptsFor(d.ID)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Error == "" {
		t.Fatal("a transport failure recorded no error")
	}
	if strings.Contains(attempts[0].Error, "super-secret-token") {
		t.Fatalf("attempt error leaked the endpoint URL: %q", attempts[0].Error)
	}
	if attempts[0].ResponseStatus != nil {
		t.Fatalf("response_status = %v, want nil on a transport error", attempts[0].ResponseStatus)
	}
}

// Ping is delivery-only: it is sent regardless of subscription, so it is the
// setup check that gives a new endpoint its first terminal delivery (spec §3).
func TestPingIsDeliveredRegardlessOfSubscription(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusOK)
	e := st.seedEndpoint("whe_9", "prj_1", rec.url(), "s", []string{domain.EventBackupFailed}, true)

	d, err := m.Ping(context.Background(), e)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	rec.waitFor(t, 1)
	if d.EventType != domain.EventWebhookPing {
		t.Fatalf("event = %q, want webhook.ping", d.EventType)
	}
	if got := rec.captured()[0].Event; got != domain.EventWebhookPing {
		t.Fatalf("event header = %q", got)
	}
	waitForStatus(t, st, d.ID, domain.DeliverySucceeded)

	svc := NewService(st, identitySealer{}, m)
	v, err := svc.View(context.Background(), e.ID)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Health != domain.EndpointHealthy {
		t.Fatalf("health after a successful ping = %q, want healthy", v.Health)
	}
}

// Backoff stays inside the documented ±20 % window for every step.
func TestBackoffJitterStaysInsideItsWindow(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	for _, j := range []float64{0, 0.5, 0.999} {
		m.jitter = func() float64 { return j }
		for n := 1; n <= maxAttempts; n++ {
			got := m.backoffFor(n)
			base := backoff[min(n-1, len(backoff)-1)]
			lo := time.Duration(float64(base) * (1 - jitterFraction))
			hi := time.Duration(float64(base) * (1 + jitterFraction))
			if got < lo || got > hi {
				t.Fatalf("backoffFor(%d) with jitter %v = %v, want within [%v, %v]", n, j, got, lo, hi)
			}
		}
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

// enqueueForTest writes a deploy.failed delivery for e without attempting it,
// so a test drives the attempt sequence itself.
func enqueueForTest(t *testing.T, m *Manager, e domain.WebhookEndpoint) domain.WebhookDelivery {
	t.Helper()
	d, err := m.enqueue(context.Background(), e, event{
		Type:     domain.EventDeployFailed,
		Resource: resourceRef{Kind: domain.WebhookResourceApplication, ID: "app_1", Name: "web"},
		Data:     eventData{DeploymentID: "dep_1", RevisionID: "rev_1", Status: string(domain.DeployFailed)},
	}, namedRef{ID: "prj_1", Name: "atlas-crm"}, namedRef{ID: "env_1", Name: "production"}, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return d
}

func attempt(t *testing.T, m *Manager, e domain.WebhookEndpoint, d domain.WebhookDelivery) domain.WebhookDelivery {
	t.Helper()
	out, err := m.Attempt(context.Background(), e, d)
	if err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	return out
}

func assertNextIn(t *testing.T, d domain.WebhookDelivery, from time.Time, base time.Duration) {
	t.Helper()
	if d.NextAttemptAt == nil {
		t.Fatal("a pending delivery must carry a next_attempt_at")
	}
	got := d.NextAttemptAt.Sub(from)
	lo := time.Duration(float64(base) * (1 - jitterFraction))
	hi := time.Duration(float64(base) * (1 + jitterFraction))
	if got < lo || got > hi {
		t.Fatalf("next attempt in %v, want within [%v, %v]", got, lo, hi)
	}
}

// waitForStatus polls a delivery until it reaches want, for the detached paths.
func waitForStatus(t *testing.T, st *fakeStore, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err := st.GetWebhookDelivery(context.Background(), id)
		if err == nil && d.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	d, _ := st.GetWebhookDelivery(context.Background(), id)
	t.Fatalf("delivery %s status = %q, want %q", id, d.Status, want)
}

// A freshly enqueued delivery is LEASED to the first attempt, not left due the
// instant it exists. Before this, next_attempt_at was `now`, so a sweeper tick
// landing while the detached first attempt was still in flight picked the same
// row up and sent a second, concurrent POST of the same signed body — spec §8
// acceptance 1 says the receiver gets exactly one.
func TestEnqueuedDeliveryIsNotImmediatelyDue(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusOK)
	e := st.seedEndpoint("whe_race_1", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	d := enqueueForTest(t, m, e)
	if d.NextAttemptAt == nil || !d.NextAttemptAt.After(c.now()) {
		t.Fatalf("a new delivery is due at %v with the clock at %v; want a lease into the future", d.NextAttemptAt, c.now())
	}
	// A sweep landing before the first attempt has finished must not touch it.
	m.SweepDue(context.Background())
	if n := len(rec.captured()); n != 0 {
		t.Fatalf("the sweeper made %d requests for a leased delivery, want 0", n)
	}
}

// The progress write is a compare-and-set on the attempt the worker started
// from. This is the second lock: even if two workers do hold the same delivery,
// the loser's write must not land — otherwise it flips a delivery that already
// SUCCEEDED back to pending and the receiver gets the whole retry ladder again.
func TestStaleProgressWriteCannotResurrectASucceededDelivery(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	m := newTestManager(st, c)
	rec := newReceiver(t, http.StatusOK)
	e := st.seedEndpoint("whe_race_2", "prj_1", rec.url(), "s", []string{domain.EventDeployFailed}, true)

	// Worker A reads the delivery at attempt 0 and succeeds it.
	d := enqueueForTest(t, m, e)
	stale := d // worker B's copy, read at the same attempt
	if got := attempt(t, m, e, d); got.Status != domain.DeliverySucceeded {
		t.Fatalf("status = %s, want succeeded", got.Status)
	}

	// Worker B now finishes its own (duplicate) attempt against its stale copy.
	// Its write is refused, and Attempt reports no error: losing the race is a
	// normal outcome, not a store failure.
	if _, err := m.advance(context.Background(), stale, domain.DeliveryPending, 1, &[]time.Time{c.now().Add(time.Minute)}[0]); err != nil {
		t.Fatalf("a losing worker returned an error: %v", err)
	}
	after, err := st.GetWebhookDelivery(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("GetWebhookDelivery: %v", err)
	}
	if after.Status != domain.DeliverySucceeded || after.NextAttemptAt != nil {
		t.Fatalf("delivery = %s next=%v; a stale write resurrected a succeeded delivery", after.Status, after.NextAttemptAt)
	}
}
