// Package logring keeps the last N lines of the panel's own log in memory, so
// GET /api/v1/panel/logs can hand an owner a bounded tail without a file, a
// shell, or a log shipper (control-plane-hardening.md §4). It is a second
// audience on the one slog pipeline: main installs a text handler writing
// into a Ring beside the JSON handler writing to stderr, joined by Fanout, so
// both see exactly the same records — and the same rule 20 discipline that
// keeps secrets out of stderr keeps them out of the tail.
package logring

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
)

// Ring is a fixed-capacity buffer of rendered log lines. It implements
// io.Writer so a slog.TextHandler can write into it: the handler emits one
// record per Write, newline-terminated, which is the line boundary here.
type Ring struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

// New returns a ring holding the most recent capacity lines. capacity must be
// positive; a non-positive value is treated as 1 so the writer never panics.
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{lines: make([]string, capacity)}
}

// Capacity is the number of lines the ring keeps.
func (r *Ring) Capacity() int { return len(r.lines) }

// Write appends each newline-terminated line in p. A trailing fragment with
// no newline is kept as its own line rather than dropped, so a writer that
// does not terminate its last line still lands.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rest := p
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		var line []byte
		if i < 0 {
			line, rest = rest, nil
		} else {
			line, rest = rest[:i], rest[i+1:]
		}
		if len(line) == 0 {
			continue
		}
		r.lines[r.next] = string(line)
		r.next = (r.next + 1) % len(r.lines)
		if r.next == 0 {
			r.full = true
		}
	}
	return len(p), nil
}

// Tail returns the most recent n lines, oldest first. n above the capacity or
// the number of lines held returns what there is; n <= 0 returns none. The
// result is a fresh slice.
func (r *Ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	held := r.next
	start := 0
	if r.full {
		held = len(r.lines)
		start = r.next
	}
	if n > held {
		n = held
	}
	if n <= 0 {
		return []string{}
	}
	out := make([]string, 0, n)
	for i := held - n; i < held; i++ {
		out = append(out, r.lines[(start+i)%len(r.lines)])
	}
	return out
}

// Handler returns a text slog handler that renders records into the ring —
// the same key=value rendering slog.NewTextHandler gives a terminal.
func (r *Ring) Handler(opts *slog.HandlerOptions) slog.Handler {
	return slog.NewTextHandler(r, opts)
}

// Fanout returns a handler that delivers every record to each of handlers.
// Enabled is true when any of them is; a record is cloned per handler so one
// cannot mutate what another sees; errors are joined.
func Fanout(handlers ...slog.Handler) slog.Handler {
	return &fanout{handlers: handlers}
}

type fanout struct {
	handlers []slog.Handler
}

func (f *fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanout) Handle(ctx context.Context, rec slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, rec.Level) {
			continue
		}
		if err := h.Handle(ctx, rec.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &fanout{handlers: out}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		out[i] = h.WithGroup(name)
	}
	return &fanout{handlers: out}
}
