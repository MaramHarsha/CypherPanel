package cron

// Cron runner tests (scheduled-tasks.md, ADR-011): arming from desired state,
// exec confined to the app's own container, run observations, overlap guard,
// and the no-container skip.

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	robfig "github.com/robfig/cron/v3"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeExec struct {
	mu         sync.Mutex
	container  string
	hasRunning bool
	resolveErr error

	execCalls [][]string
	exit      int
	out       []byte
	execErr   error
	block     chan struct{} // when non-nil, ExecAndWait waits on it
}

func (f *fakeExec) RunningContainerForApp(_ context.Context, _ string) (string, bool, error) {
	return f.container, f.hasRunning, f.resolveErr
}

func (f *fakeExec) ExecAndWait(_ context.Context, _ string, argv []string) (int, []byte, error) {
	f.mu.Lock()
	f.execCalls = append(f.execCalls, argv)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return f.exit, f.out, f.execErr
}

func (f *fakeExec) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.execCalls...)
}

type fakePub struct {
	mu   sync.Mutex
	runs []*agentv1.ScheduledTaskRun
}

func (p *fakePub) Publish(_ string, data []byte) error {
	var run agentv1.ScheduledTaskRun
	if err := proto.Unmarshal(data, &run); err != nil {
		return err
	}
	p.mu.Lock()
	p.runs = append(p.runs, &run)
	p.mu.Unlock()
	return nil
}

func (p *fakePub) last() *agentv1.ScheduledTaskRun {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.runs) == 0 {
		return nil
	}
	return p.runs[len(p.runs)-1]
}

func newRunner(exec Executor, pub Publisher) *Runner {
	return New(exec, pub, "srv1", quietLog())
}

func everyMinute(t *testing.T) robfig.Schedule {
	t.Helper()
	s, err := robfig.ParseStandard("* * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

func TestExecAndReportRunsInContainerAndPublishes(t *testing.T) {
	ex := &fakeExec{container: "c1", hasRunning: true, exit: 0, out: []byte("ok")}
	pub := &fakePub{}
	r := newRunner(ex, pub)

	r.execAndReport(context.Background(), "sch_1", entry{appID: "app1", command: []string{"sh", "-c", "echo ok"}})

	calls := ex.calls()
	if len(calls) != 1 || calls[0][0] != "sh" {
		t.Fatalf("exec calls = %v, want the argv once", calls)
	}
	run := pub.last()
	if run == nil || run.GetTaskId() != "sch_1" || run.GetFailed() || run.GetExitCode() != 0 {
		t.Fatalf("run = %+v, want a succeeded run for sch_1", run)
	}
	if run.GetOutputTail() != "ok" {
		t.Fatalf("output tail = %q, want captured output", run.GetOutputTail())
	}
	if run.GetRunId() == "" {
		t.Fatal("run_id must be set (idempotency key)")
	}
}

func TestExecAndReportNonZeroExitIsFailed(t *testing.T) {
	ex := &fakeExec{container: "c1", hasRunning: true, exit: 2, out: []byte("boom")}
	pub := &fakePub{}
	r := newRunner(ex, pub)

	r.execAndReport(context.Background(), "sch_1", entry{appID: "app1", command: []string{"false"}})
	run := pub.last()
	if run == nil || !run.GetFailed() || run.GetExitCode() != 2 {
		t.Fatalf("run = %+v, want failed with exit 2", run)
	}
}

func TestExecAndReportNoContainerSkips(t *testing.T) {
	ex := &fakeExec{hasRunning: false}
	pub := &fakePub{}
	r := newRunner(ex, pub)

	r.execAndReport(context.Background(), "sch_1", entry{appID: "app1", command: []string{"true"}})
	if len(ex.calls()) != 0 {
		t.Fatal("must not exec when no container is running")
	}
	if pub.last() != nil {
		t.Fatal("a skipped run must not be reported")
	}
}

func TestExecErrorSurfacesInTail(t *testing.T) {
	ex := &fakeExec{container: "c1", hasRunning: true, execErr: errString("dial fail")}
	pub := &fakePub{}
	r := newRunner(ex, pub)

	r.execAndReport(context.Background(), "sch_1", entry{appID: "app1", command: []string{"true"}})
	run := pub.last()
	if run == nil || !run.GetFailed() {
		t.Fatalf("run = %+v, want failed on exec error", run)
	}
	if run.GetOutputTail() == "" {
		t.Fatal("exec error should surface in the output tail")
	}
}

func TestSyncArmsTasksAndPreservesUnchanged(t *testing.T) {
	r := newRunner(&fakeExec{}, &fakePub{})
	spec := &agentv1.AppSpec{AppId: "app1", ScheduledTasks: []*agentv1.ScheduledTask{
		{Id: "sch_1", Schedule: "* * * * *", Command: []string{"true"}},
	}}
	r.Sync([]*agentv1.AppSpec{spec})

	r.mu.Lock()
	e, ok := r.armed["sch_1"]
	firstNext := time.Time{}
	if ok {
		firstNext = e.next
	}
	r.mu.Unlock()
	if !ok {
		t.Fatal("task not armed after Sync")
	}

	// Re-Sync with the same task: the next-fire time is preserved.
	r.Sync([]*agentv1.AppSpec{spec})
	r.mu.Lock()
	if r.armed["sch_1"].next != firstNext {
		t.Error("unchanged task should keep its next-fire time")
	}
	r.mu.Unlock()

	// A task no longer present is dropped.
	r.Sync([]*agentv1.AppSpec{{AppId: "app1"}})
	r.mu.Lock()
	_, still := r.armed["sch_1"]
	r.mu.Unlock()
	if still {
		t.Error("removed task should be dropped from the armed set")
	}
}

func TestFireDueGuardsOverlap(t *testing.T) {
	release := make(chan struct{})
	ex := &fakeExec{container: "c1", hasRunning: true, block: release}
	r := newRunner(ex, &fakePub{})

	// Arm one task already due (next in the past); a second fire must not
	// double-run it while the first is still in flight.
	r.mu.Lock()
	r.armed["sch_1"] = &entry{appID: "app1", schedule: "* * * * *", command: []string{"sleep"}, parsed: everyMinute(t), next: time.Now().Add(-time.Minute)}
	r.mu.Unlock()

	r.fireDue(context.Background())
	// The first fire marked it running and advanced its next time.
	r.mu.Lock()
	running := r.running["sch_1"]
	r.mu.Unlock()
	if !running {
		t.Fatal("first fire should mark the task running")
	}

	// Second fire while still running: no second exec dispatched.
	r.fireDue(context.Background())
	close(release)
	// Give the goroutine(s) a moment to finish and record their exec calls.
	waitUntil(t, func() bool { return len(ex.calls()) >= 1 })
	if n := len(ex.calls()); n != 1 {
		t.Fatalf("exec calls = %d, want exactly 1 (overlap skipped)", n)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not met before deadline")
	}
}
