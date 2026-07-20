// Package cron is the agent-side scheduled-task reconciler (scheduled-tasks.md,
// ADR-011). It owns the clock: tasks arrive as declarative desired state on the
// app's AppSpec, and this runner evaluates each schedule against the host clock
// and execs the task's argv command inside the app's own container when due —
// there is no plane "run now" verb. Each run's terminal outcome is published
// back as a ScheduledTaskRun observation (ADR-005).
package cron

import (
	"context"
	"log/slog"
	"sync"
	"time"

	robfig "github.com/robfig/cron/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/MaramHarsha/cypherpanel/pkg/ids"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// tickInterval is how often due tasks are checked. Finer than cron's 1-minute
// resolution so no minute is missed.
const tickInterval = 30 * time.Second

// outputTailCap bounds the captured stdout+stderr per run (scheduled-tasks.md
// §6).
const outputTailCap = 4 << 10

// Executor runs a task in its app's own container (consumer-defined;
// *docker.Driver satisfies it).
type Executor interface {
	// RunningContainerForApp returns the app's running container, or ok=false
	// when none is running (the run is skipped).
	RunningContainerForApp(ctx context.Context, appID string) (containerID string, ok bool, err error)
	// ExecAndWait runs argv to completion, returning the exit code and output.
	ExecAndWait(ctx context.Context, containerID string, argv []string) (exitCode int, output []byte, err error)
}

// Publisher publishes a ScheduledTaskRun observation (consumer-defined;
// *bus.Bus satisfies it).
type Publisher interface {
	Publish(subject string, data []byte) error
}

// entry is one armed task: its parsed schedule and next fire time.
type entry struct {
	appID    string
	schedule string
	command  []string
	parsed   robfig.Schedule
	next     time.Time
}

// Runner arms tasks from desired state and fires them on schedule. Construct
// with New; drive with Sync (on every reconcile) and Run (a background loop).
type Runner struct {
	exec     Executor
	pub      Publisher
	serverID string
	log      *slog.Logger
	now      func() time.Time
	parser   robfig.Parser

	mu      sync.Mutex
	armed   map[string]*entry // by task id
	running map[string]bool   // task ids currently executing (overlap guard)
}

// New wires the runner.
func New(exec Executor, pub Publisher, serverID string, log *slog.Logger) *Runner {
	return &Runner{
		exec:     exec,
		pub:      pub,
		serverID: serverID,
		log:      log,
		now:      time.Now,
		parser:   robfig.NewParser(robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow),
		armed:    map[string]*entry{},
		running:  map[string]bool{},
	}
}

// Sync re-arms the task set from the current desired specs. Unchanged tasks keep
// their next-fire time; new or edited tasks are (re)armed from now; removed
// tasks are dropped. Called after every reconcile, so create/edit/delete/enable
// changes are picked up (scheduled-tasks.md §5).
func (r *Runner) Sync(specs []*agentv1.AppSpec) {
	now := r.now()
	next := make(map[string]*entry)
	for _, spec := range specs {
		for _, t := range spec.GetScheduledTasks() {
			if prev, ok := r.matchUnchanged(t, spec.GetAppId()); ok {
				next[t.GetId()] = prev
				continue
			}
			sched, err := r.parser.Parse(t.GetSchedule())
			if err != nil {
				// The plane validates schedules at create time; a parse failure
				// here is defensive — skip this task rather than crash the loop.
				r.log.Error("cron: unparseable schedule; task skipped", "task_id", t.GetId(), "schedule", t.GetSchedule(), "error", err)
				continue
			}
			next[t.GetId()] = &entry{
				appID:    spec.GetAppId(),
				schedule: t.GetSchedule(),
				command:  t.GetCommand(),
				parsed:   sched,
				next:     sched.Next(now),
			}
		}
	}
	r.mu.Lock()
	r.armed = next
	r.mu.Unlock()
}

// matchUnchanged returns the existing entry when a task's schedule and command
// are unchanged, so its next-fire time is preserved across a Sync.
func (r *Runner) matchUnchanged(t *agentv1.ScheduledTask, appID string) (*entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, ok := r.armed[t.GetId()]
	if !ok || prev.appID != appID || prev.schedule != t.GetSchedule() || !equalArgs(prev.command, t.GetCommand()) {
		return nil, false
	}
	return prev, true
}

// Run is the fire loop: on each tick, execute every task whose time has come and
// that is not already running. Returns when ctx is done.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.fireDue(ctx)
		}
	}
}

// fireDue launches every armed task whose next-fire time has passed and that is
// not already running, advancing each to its next occurrence.
func (r *Runner) fireDue(ctx context.Context) {
	now := r.now()
	type due struct {
		taskID string
		e      entry
	}
	var toRun []due

	r.mu.Lock()
	for id, e := range r.armed {
		if now.Before(e.next) || r.running[id] {
			continue
		}
		r.running[id] = true
		toRun = append(toRun, due{taskID: id, e: *e})
		e.next = e.parsed.Next(now) // advance in place; overlap guarded by running
	}
	r.mu.Unlock()

	for _, d := range toRun {
		go r.execAndReport(ctx, d.taskID, d.e)
	}
}

// execAndReport runs one task in its app container and publishes the outcome. A
// missing container is a skip (no observation); anything else — including a
// non-zero exit or an exec error — is a reported run.
func (r *Runner) execAndReport(ctx context.Context, taskID string, e entry) {
	defer func() {
		r.mu.Lock()
		delete(r.running, taskID)
		r.mu.Unlock()
	}()

	started := r.now()
	containerID, ok, err := r.exec.RunningContainerForApp(ctx, e.appID)
	if err != nil {
		r.log.Error("cron: resolving container", "task_id", taskID, "app_id", e.appID, "error", err)
		return
	}
	if !ok {
		r.log.Debug("cron: no running container; task run skipped", "task_id", taskID, "app_id", e.appID)
		return
	}

	exit, output, err := r.exec.ExecAndWait(ctx, containerID, e.command)
	failed := err != nil || exit != 0
	detail := output
	if err != nil {
		// An exec transport error (not a non-zero exit): surface its text in the
		// output tail so the operator sees why nothing ran.
		detail = append(detail, []byte("\n"+err.Error())...)
	}

	run := &agentv1.ScheduledTaskRun{
		TaskId:     taskID,
		RunId:      ids.New(ids.PrefixTaskRun),
		StartedAt:  timestamppb.New(started),
		FinishedAt: timestamppb.New(r.now()),
		ExitCode:   int32(exit),
		Failed:     failed,
		OutputTail: tail(detail, outputTailCap),
	}
	data, merr := proto.Marshal(run)
	if merr != nil {
		r.log.Error("cron: marshaling run", "task_id", taskID, "error", merr)
		return
	}
	if perr := r.pub.Publish(subjects.TaskState(r.serverID), data); perr != nil {
		r.log.Error("cron: publishing run", "task_id", taskID, "error", perr)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tail returns the last cap bytes of b as a string.
func tail(b []byte, cap int) string {
	if len(b) <= cap {
		return string(b)
	}
	return string(b[len(b)-cap:])
}
