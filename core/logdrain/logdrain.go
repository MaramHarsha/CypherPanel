// Package logdrain ships the panel's runtime log lines onward to a system built
// for longer retention and search (log-drains.md).
//
// THE THREE REFUSALS this package is built around:
//
//  1. IT MUST NEVER BLOCK OR SLOW A DEPLOY, structurally rather than by
//     discipline. The drain is a READER of a stream the deploy path writes to:
//     no call site in the scheduler, nothing awaited. Deleting this package
//     from the binary would change no other package's behaviour.
//
//  2. IT MUST NEVER GROW UNBOUNDED, and it needs no new storage to promise it.
//     A failing sink means the batch is not acked, so the cursor does not
//     advance, so the backlog stays in RUNTIME_LOGS — already file-backed,
//     already capped at 24h and 512 MiB, already DiscardOld. The buffer already
//     exists, is already bounded, and is already paid for. The shape this repo
//     would otherwise reach for — a deliveries table with attempts, like
//     outbound webhooks — is right for an event arriving once a deploy and
//     catastrophic for one arriving thousands of times a second: a row per log
//     line is a disk fill written by us, on purpose.
//
//  3. IT MUST NEVER BECOME A SECOND LOG STORE ON THE PLANE. Long retention and
//     complex queries stay off-platform.
package logdrain

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Defaults for the batching knobs.
const (
	DefaultBatchLines    = 500
	DefaultBatchInterval = 5 * time.Second
	DefaultMaxBackoff    = time.Minute
	// sweepInterval is how often the Manager reconciles its goroutines against
	// the drains in Postgres.
	sweepInterval = 15 * time.Second
)

// Store is the persistence the manager needs (consumer-defined).
type Store interface {
	ListEnabledLogDrains(ctx context.Context) ([]domain.LogDrain, error)
	RecordLogDrainShipped(ctx context.Context, id string, at time.Time) error
	RecordLogDrainError(ctx context.Context, id, detail string, at time.Time) error
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)
	GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error)
}

// Bus is the durable-consumer surface (consumer-defined).
type Bus interface {
	ConsumeRuntimeLogs(ctx context.Context, durable string, handle func(subject string, data []byte, ack func())) (ConsumeContext, error)
	DeleteRuntimeLogConsumer(ctx context.Context, durable string) error
}

// ConsumeContext is what a running consumer hands back so it can be stopped.
type ConsumeContext interface{ Stop() }

// Opener unseals a drain's config.
type Opener interface {
	Open(ct, nonce []byte) ([]byte, error)
}

// Record is one shipped line. The agent publishes a bare, already-normalised
// line — the 8-byte Docker frame header is gone, a trailing \r is stripped and
// an empty line is dropped — so the record is COMPOSED here.
//
// `ts` is PLANE RECEIVE TIME and the docs say so. Skew is agent-to-plane
// transit — milliseconds, except when an agent reconnects and replays a hundred
// old lines stamped now. Re-deriving emit time by parsing the line is what every
// log platform tries and gets wrong on somebody's format.
type Record struct {
	TS          time.Time `json:"ts"`
	Line        string    `json:"line"`
	Server      string    `json:"server"`
	App         string    `json:"app"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	AppID       string    `json:"app_id"`
	ProjectID   string    `json:"project_id"`
}

// Sink is one transport. Accepting a batch is what advances the cursor, so a
// sink that returns an error is the entire backpressure mechanism.
type Sink interface {
	Ship(ctx context.Context, records []Record) error
	Close() error
}

// Options wires the manager.
type Options struct {
	Store         Store
	Bus           Bus
	Opener        Opener
	BatchLines    int
	BatchInterval time.Duration
	MaxBackoff    time.Duration
	Log           *slog.Logger
	Now           func() time.Time
	// NewSink builds a transport for a drain. Injected so every sink is
	// testable without a Loki.
	NewSink func(d domain.LogDrain, cfg []byte, target domain.BackupTarget) (Sink, error)
}

// Manager is a RECONCILER, not an event handler: each sweep it diffs enabled
// drains in Postgres against the goroutines and consumers it runs, starts what
// is missing, and stops what is gone. A drain created through the API starts
// within a tick with no restart; a plane that crashed mid-change converges on
// boot.
type Manager struct {
	o Options

	mu      sync.Mutex
	running map[string]*shipper
}

func New(o Options) *Manager {
	if o.BatchLines <= 0 {
		o.BatchLines = DefaultBatchLines
	}
	if o.BatchInterval <= 0 {
		o.BatchInterval = DefaultBatchInterval
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = DefaultMaxBackoff
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewSink == nil {
		o.NewSink = defaultSink
	}
	return &Manager{o: o, running: map[string]*shipper{}}
}

// Run is the one owned goroutine (ENGINEERING rule 7).
func (m *Manager) Run(ctx context.Context) {
	m.sweep(ctx)
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-t.C:
			m.sweep(ctx)
		}
	}
}

func (m *Manager) sweep(ctx context.Context) {
	drains, err := m.o.Store.ListEnabledLogDrains(ctx)
	if err != nil {
		m.o.Log.Error("log drains: listing", "error", err)
		return
	}
	want := make(map[string]domain.LogDrain, len(drains))
	for _, d := range drains {
		want[d.ID] = d
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, sh := range m.running {
		d, still := want[id]
		if !still || sh.revision != d.UpdatedAt.UnixNano() {
			// Gone, or changed: stop it and let the start loop below rebuild
			// it from the current row. Restarting a shipper is cheap — the
			// cursor is durable and lives on the server.
			sh.stop()
			delete(m.running, id)
		}
	}
	for id, d := range want {
		if _, ok := m.running[id]; ok {
			continue
		}
		sh, err := m.start(ctx, d)
		if err != nil {
			m.o.Log.Error("log drains: starting", "drain_id", id, "error", err)
			_ = m.o.Store.RecordLogDrainError(ctx, id, err.Error(), m.o.Now())
			continue
		}
		m.running[id] = sh
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sh := range m.running {
		sh.stop()
		delete(m.running, id)
	}
}

// Durable is the consumer name for a drain: one drain, one cursor.
func Durable(drainID string) string { return "drain-" + drainID }

// Forget removes a deleted drain's cursor. Called by the service on delete so
// a durable consumer does not outlive the drain that owned it and hold the
// stream's ack floor down forever.
func (m *Manager) Forget(ctx context.Context, drainID string) {
	m.mu.Lock()
	if sh, ok := m.running[drainID]; ok {
		sh.stop()
		delete(m.running, drainID)
	}
	m.mu.Unlock()
	if err := m.o.Bus.DeleteRuntimeLogConsumer(ctx, Durable(drainID)); err != nil {
		m.o.Log.Debug("log drains: deleting a consumer", "drain_id", drainID, "error", err)
	}
}

func (m *Manager) start(ctx context.Context, d domain.LogDrain) (*shipper, error) {
	cfg, err := m.o.Opener.Open(d.ConfigCT, d.ConfigNonce)
	if err != nil {
		return nil, err
	}
	var target domain.BackupTarget
	if d.Kind == domain.DrainS3 && d.TargetID != "" {
		target, err = m.o.Store.GetBackupTarget(ctx, d.TargetID)
		if err != nil {
			return nil, err
		}
	}
	sink, err := m.o.NewSink(d, cfg, target)
	if err != nil {
		return nil, err
	}

	sctx, cancel := context.WithCancel(ctx)
	sh := &shipper{
		m: m, drain: d, sink: sink, cancel: cancel,
		revision: d.UpdatedAt.UnixNano(),
		lines:    make(chan pending, m.o.BatchLines*2),
	}
	cc, err := m.o.Bus.ConsumeRuntimeLogs(sctx, Durable(d.ID), sh.receive)
	if err != nil {
		cancel()
		_ = sink.Close()
		return nil, err
	}
	sh.consume = cc
	go sh.run(sctx)
	return sh, nil
}

type pending struct {
	rec Record
	ack func()
}

type shipper struct {
	m        *Manager
	drain    domain.LogDrain
	sink     Sink
	consume  ConsumeContext
	cancel   context.CancelFunc
	revision int64
	lines    chan pending
}

func (s *shipper) stop() {
	s.cancel()
	if s.consume != nil {
		s.consume.Stop()
	}
	_ = s.sink.Close()
}

// receive turns one message into a record and queues it. It NEVER acks here:
// the ack is what advances the cursor, and it belongs after the sink accepts.
func (s *shipper) receive(subject string, data []byte, ack func()) {
	server, appID := parseSubject(subject)
	if appID == "" {
		ack() // not a shape we can attribute; do not hold the cursor for it
		return
	}
	rec := Record{
		TS: s.m.o.Now().UTC(), Line: string(data),
		Server: server, AppID: appID,
	}
	s.m.enrich(context.Background(), &rec)
	// Scope: all projects, or one. Nothing finer — every line carries its
	// environment as a label, so a sink filters better than we can.
	if s.drain.ProjectID != "" && rec.ProjectID != s.drain.ProjectID {
		ack()
		return
	}
	select {
	case s.lines <- pending{rec: rec, ack: ack}:
	default:
		// The in-process queue is full because the sink is behind. Do NOT ack:
		// the message stays on the stream, which is the bounded buffer, and
		// JetStream redelivers it after AckWait.
	}
}

func (s *shipper) run(ctx context.Context) {
	batch := make([]Record, 0, s.m.o.BatchLines)
	acks := make([]func(), 0, s.m.o.BatchLines)
	t := time.NewTicker(s.m.o.BatchInterval)
	defer t.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if s.ship(ctx, batch) {
			// Acked only after the sink accepted. That one rule IS the entire
			// backpressure design.
			for _, ack := range acks {
				ack()
			}
		}
		batch = batch[:0]
		acks = acks[:0]
	}

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-s.lines:
			batch = append(batch, p.rec)
			acks = append(acks, p.ack)
			if len(batch) >= s.m.o.BatchLines {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

// ship retries with exponential backoff and full jitter, capped — far shorter
// than the outbound-webhook horizon, because there sleeping is free and here
// every second asleep spends the retention window that is acting as the buffer.
//
// A FAILING DRAIN IS NEVER AUTO-DISABLED. It retries at the cap forever, and
// the panel says which state it is in.
func (s *shipper) ship(ctx context.Context, batch []Record) bool {
	backoff := time.Second
	for {
		err := s.sink.Ship(ctx, batch)
		if err == nil {
			if rerr := s.m.o.Store.RecordLogDrainShipped(ctx, s.drain.ID, s.m.o.Now()); rerr != nil {
				s.m.o.Log.Debug("log drains: recording a shipped batch", "drain_id", s.drain.ID, "error", rerr)
			}
			return true
		}
		if rerr := s.m.o.Store.RecordLogDrainError(ctx, s.drain.ID, truncate(err.Error(), 500), s.m.o.Now()); rerr != nil {
			s.m.o.Log.Debug("log drains: recording a failure", "drain_id", s.drain.ID, "error", rerr)
		}
		s.m.o.Log.Warn("log drains: shipping failed", "drain_id", s.drain.ID, "lines", len(batch), "error", err)

		jittered := time.Duration(rand.Int63n(int64(backoff)) + 1)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(jittered):
		}
		if backoff < s.m.o.MaxBackoff {
			backoff *= 2
			if backoff > s.m.o.MaxBackoff {
				backoff = s.m.o.MaxBackoff
			}
		}
	}
}

// enrich resolves the names a sink labels by. Best-effort: an application that
// was deleted between the line and the ship still ships, with the ids it has —
// dropping a line because its owner is gone would lose exactly the lines
// somebody is looking for.
func (m *Manager) enrich(ctx context.Context, rec *Record) {
	app, err := m.o.Store.GetApplication(ctx, rec.AppID)
	if err != nil {
		return
	}
	rec.App = app.Name
	env, err := m.o.Store.GetEnvironment(ctx, app.EnvironmentID)
	if err != nil {
		return
	}
	rec.Environment = env.Name
	rec.ProjectID = env.ProjectID
	if proj, err := m.o.Store.GetProject(ctx, env.ProjectID); err == nil {
		rec.Project = proj.Name
	}
}

// parseSubject reads `logs.<server>.runtime.<app>`.
func parseSubject(subject string) (server, appID string) {
	parts := strings.Split(subject, ".")
	if len(parts) < 4 || parts[0] != "logs" || parts[2] != "runtime" {
		return "", ""
	}
	return parts[1], parts[3]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// MarshalRecords is the JSON-lines body two of the three sinks use.
func MarshalRecords(records []Record) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return []byte(b.String()), nil
}
