package logring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// TestRingKeepsTheMostRecentLinesOldestFirst: below capacity every line is
// held; past it the oldest go, and Tail returns in write order.
func TestRingKeepsTheMostRecentLinesOldestFirst(t *testing.T) {
	r := New(3)
	if got := r.Tail(5); len(got) != 0 {
		t.Fatalf("empty ring Tail = %v", got)
	}
	for i := 1; i <= 2; i++ {
		fmt.Fprintf(r, "line %d\n", i)
	}
	if got := strings.Join(r.Tail(10), "|"); got != "line 1|line 2" {
		t.Fatalf("Tail below capacity = %q", got)
	}
	for i := 3; i <= 5; i++ {
		fmt.Fprintf(r, "line %d\n", i)
	}
	if got := strings.Join(r.Tail(10), "|"); got != "line 3|line 4|line 5" {
		t.Fatalf("Tail after wrap = %q", got)
	}
	if got := strings.Join(r.Tail(2), "|"); got != "line 4|line 5" {
		t.Fatalf("Tail(2) = %q", got)
	}
	if got := r.Tail(0); len(got) != 0 {
		t.Fatalf("Tail(0) = %v", got)
	}
	if r.Capacity() != 3 || New(0).Capacity() != 1 {
		t.Fatal("capacity bookkeeping is off")
	}
}

// One Write may carry several lines or none of its newline; both land.
func TestWriteSplitsOnNewlinesAndKeepsAFragment(t *testing.T) {
	r := New(4)
	_, _ = r.Write([]byte("a\nb\n\nc"))
	if got := strings.Join(r.Tail(4), "|"); got != "a|b|c" {
		t.Fatalf("Tail = %q (blank lines dropped, fragment kept)", got)
	}
}

// TestHandlerRendersStructuredRecords: the slog text handler feeds the ring
// with key=value lines, attrs and groups included.
func TestHandlerRendersStructuredRecords(t *testing.T) {
	r := New(10)
	log := slog.New(r.Handler(&slog.HandlerOptions{Level: slog.LevelInfo}))
	log.With("component", "test").Info("http request", "method", "GET", "status", 500)
	log.Debug("hidden below the level")
	lines := r.Tail(10)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want exactly the info record", lines)
	}
	for _, want := range []string{`level=INFO`, `msg="http request"`, `component=test`, `method=GET`, `status=500`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("line %q lacks %q", lines[0], want)
		}
	}
}

// failingHandler errors on every record, to prove Fanout joins errors and
// keeps delivering to the others.
type failingHandler struct{}

func (failingHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (failingHandler) Handle(context.Context, slog.Record) error { return errors.New("boom") }
func (f failingHandler) WithAttrs([]slog.Attr) slog.Handler      { return f }
func (f failingHandler) WithGroup(string) slog.Handler           { return f }

// TestFanoutDeliversToEveryHandler: both sinks see the same record, with the
// same attrs and groups, and one failing sink neither hides the record from
// the other nor swallows its own error.
func TestFanoutDeliversToEveryHandler(t *testing.T) {
	var stderr bytes.Buffer
	ring := New(5)
	h := Fanout(slog.NewJSONHandler(&stderr, nil), ring.Handler(nil), failingHandler{})
	log := slog.New(h).With("server_id", "srv_1").WithGroup("req")
	log.Info("hello", "path", "/x")

	if !strings.Contains(stderr.String(), `"server_id":"srv_1"`) || !strings.Contains(stderr.String(), `"req":{"path":"/x"}`) {
		t.Fatalf("json sink = %q", stderr.String())
	}
	lines := ring.Tail(5)
	if len(lines) != 1 || !strings.Contains(lines[0], "server_id=srv_1") || !strings.Contains(lines[0], "req.path=/x") {
		t.Fatalf("ring sink = %v", lines)
	}
	if err := h.Handle(context.Background(), slog.Record{Level: slog.LevelInfo, Message: "direct"}); err == nil {
		t.Fatal("the failing sink's error was swallowed")
	}
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Enabled must be true when any sink accepts the level")
	}
	quiet := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})
	if Fanout(quiet, slog.NewTextHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelError})).Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Enabled must be false when no sink accepts the level")
	}
}

// Writers from many goroutines never race or lose the ring's shape.
func TestRingIsSafeForConcurrentWriters(t *testing.T) {
	r := New(50)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				fmt.Fprintf(r, "g%d-%d\n", g, i)
			}
		}()
	}
	wg.Wait()
	if got := r.Tail(100); len(got) != 50 {
		t.Fatalf("held %d lines, want the capacity", len(got))
	}
}
