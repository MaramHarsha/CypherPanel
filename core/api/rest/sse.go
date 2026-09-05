package rest

import (
	"context"
	"fmt"
	"net/http"
)

// streamLogSSE serves one build-log subject as a Server-Sent-Events stream:
// the LOGS stream's retained history first (a client connecting mid-build sees
// what it missed — application-deploy.md §5 bounded retention), then the live
// tail until the client disconnects.
func (a *API) streamLogSSE(w http.ResponseWriter, r *http.Request, subject string) {
	a.streamSSE(w, r, subject, a.deps.Logs.SubscribeLogs)
}

// streamRuntimeLogSSE is streamLogSSE for runtime logs, backed by the
// file-backed RUNTIME_LOGS stream (bounded-log-retention.md §4): retained
// history within the retention window replays first, then the live tail.
func (a *API) streamRuntimeLogSSE(w http.ResponseWriter, r *http.Request, subject string) {
	a.streamSSE(w, r, subject, a.deps.Logs.SubscribeRuntimeLogs)
}

// streamSSE pumps one log subject to the client as SSE data frames using
// subscribe, which selects the backing stream (build vs runtime logs).
func (a *API) streamSSE(w http.ResponseWriter, r *http.Request, subject string,
	subscribe func(ctx context.Context, subject string, handle func(data []byte)) (stop func(), err error)) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ctx := r.Context()
	lines := make(chan []byte, 256)
	stop, err := subscribe(ctx, subject, func(data []byte) {
		cp := make([]byte, len(data))
		copy(cp, data)
		select {
		case lines <- cp:
		case <-ctx.Done(): // client gone; the deferred stop tears us down
		}
	})
	if err != nil {
		a.deps.Log.Error("subscribing to logs", "subject", subject, "trace_id", traceIDFromContext(ctx), "error", err)
		writeError(w, http.StatusInternalServerError, "could not subscribe to logs")
		return
	}
	defer stop()

	// Notify the client that the stream is open (there may be no lines yet).
	// The opening frame carries the request's correlation id: a stream that
	// misbehaves is diagnosed from the same id every other response gives
	// (control-plane-hardening.md §2).
	if _, err := fmt.Fprintf(w, "event: connected\ndata: {\"trace_id\":%q}\n\n", traceIDFromContext(ctx)); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case data := <-lines:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
