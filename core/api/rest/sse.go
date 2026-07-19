package rest

import (
	"fmt"
	"net/http"
)

// streamLogSSE serves one log subject as a Server-Sent-Events stream: the
// LOGS stream's retained history first (a client connecting mid-build sees
// what it missed — application-deploy.md §5 bounded retention), then the live
// tail until the client disconnects.
func (a *API) streamLogSSE(w http.ResponseWriter, r *http.Request, subject string) {
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
	stop, err := a.deps.Logs.SubscribeLogs(ctx, subject, func(data []byte) {
		cp := make([]byte, len(data))
		copy(cp, data)
		select {
		case lines <- cp:
		case <-ctx.Done(): // client gone; the deferred stop tears us down
		}
	})
	if err != nil {
		a.deps.Log.Error("subscribing to logs", "subject", subject, "error", err)
		writeError(w, http.StatusInternalServerError, "could not subscribe to logs")
		return
	}
	defer stop()

	// Notify the client that the stream is open (there may be no lines yet).
	if _, err := fmt.Fprintf(w, "event: connected\ndata: {}\n\n"); err != nil {
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
