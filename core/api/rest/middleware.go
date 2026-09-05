package rest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ─── request ids ────────────────────────────────────────────────────────────

// TraceIDHeader carries the correlation id on every response — the value a 500
// screen shows and an operator pastes into a bug report (canvases 13s, 13ai).
const TraceIDHeader = "X-Request-Id"

// traceIDMaxLen bounds a client-supplied id. A correlation id is a token, not
// a message: anything longer is either a mistake or an attempt to bloat every
// log line the request touches.
const traceIDMaxLen = 128

// newTraceID mints an id of the shape the UI shows: "trace_" and 8 random
// bytes as hex in four groups — short enough to read aloud over a phone and
// wide enough that two concurrent requests never collide. crypto/rand.Read
// never returns an error (it is documented to panic instead, since Go 1.24), so
// there is no fallback path here to leave untested.
func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	h := hex.EncodeToString(b[:])
	return "trace_" + h[0:4] + "-" + h[4:8] + "-" + h[8:12] + "-" + h[12:16]
}

// validTraceID reports whether a client-supplied id may be reused. Only short
// printable tokens are: an id reaches every log line for the request, so a
// newline or a control character in it would be log injection.
func validTraceID(s string) bool {
	if s == "" || len(s) > traceIDMaxLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

// requestID is the outermost middleware. It stamps TraceIDHeader on the
// response header map *before* any handler runs, so writeError can read the id
// back without a signature change, and puts it in the request context for
// handlers that log. An inbound id is honoured only from a trusted proxy
// (Deps.TrustedProxies) and only when it is a plain token — otherwise a client
// could choose the id its own request is filed under
// (control-plane-hardening.md §2).
func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ""
		if a.peerIsTrustedProxy(r) {
			if given := strings.TrimSpace(r.Header.Get(TraceIDHeader)); validTraceID(given) {
				id = given
			}
		}
		if id == "" {
			id = newTraceID()
		}
		w.Header().Set(TraceIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceIDKey, id)))
	})
}

// traceIDFromContext returns the request's correlation id, empty outside a
// request served through the middleware.
func traceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(traceIDKey).(string)
	return id
}

// ─── client address ─────────────────────────────────────────────────────────

// peerIsTrustedProxy reports whether the TCP peer sits inside one of the
// configured trusted-proxy CIDRs. With none configured — the default, and the
// right one for a panel exposed directly — nothing is trusted, so no header
// can speak for the client.
func (a *API) peerIsTrustedProxy(r *http.Request) bool {
	if len(a.deps.TrustedProxies) == 0 {
		return false
	}
	addr, ok := parseAddr(peerHost(r))
	if !ok {
		return false
	}
	return a.trusted(addr)
}

func (a *API) trusted(addr netip.Addr) bool {
	for _, p := range a.deps.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP is the address the panel attributes the request to — the key a rate
// limit counts against (control-plane-hardening.md §5). From an untrusted peer
// it is the TCP peer and nothing else. From a trusted proxy it is the
// right-most X-Forwarded-For entry that is not itself a trusted hop (so a
// client cannot pick its own key by prepending addresses), falling back to
// X-Real-IP and then the peer.
func (a *API) clientIP(r *http.Request) string {
	peer := peerHost(r)
	if !a.peerIsTrustedProxy(r) {
		return peer
	}
	hops := forwardedFor(r)
	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parseAddr(hops[i])
		if !ok {
			// An unparseable hop ends the walk: everything to its left is
			// whatever that entry chose to write.
			break
		}
		if !a.trusted(addr) {
			return addr.String()
		}
	}
	if real, ok := parseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ok {
		return real.String()
	}
	return peer
}

// forwardedFor splits every X-Forwarded-For header into its hops, left to
// right (the client first, each proxy appending itself).
func forwardedFor(r *http.Request) []string {
	var out []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(h, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// peerHost is the TCP peer's address without its port.
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// parseAddr accepts a bare address, a bracketed one, or one with a port, and
// drops any IPv6 zone so two spellings of one address share a rate-limit key.
func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// ─── logging, headers, recovery ─────────────────────────────────────────────

// logRequests writes one line per request. A 5xx is logged at error level with
// the trace id, the matched route pattern and the path, so the id an operator
// pastes finds the fault (control-plane-hardening.md §2).
func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"trace_id", w.Header().Get(TraceIDHeader),
		}
		if sw.status >= http.StatusInternalServerError {
			a.deps.Log.Error("http request failed", append(attrs, "route", r.Pattern)...)
			return
		}
		a.deps.Log.Info("http request", attrs...)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a handler panic into the ordinary Error envelope with 500 —
// carrying the same trace id as every other response, so a crash is as
// reportable as a refusal. It sits inside logRequests so the panic is observed
// as a 500 request line rather than vanishing (ENGINEERING rule 8: a panic
// never crosses a package boundary; this is the last net).
func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.deps.Log.Error("panic in handler",
					"method", r.Method,
					"route", r.Pattern,
					"path", r.URL.Path,
					"trace_id", w.Header().Get(TraceIDHeader),
					"panic", rec,
				)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	// wrote records that the status line has gone out, so a panic after a
	// partial response (an SSE stream, say) does not try to write a second.
	wrote bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so the SSE log endpoints can stream:
// embedding http.ResponseWriter does not promote Flush (it is not part of that
// interface), so without this the http.Flusher assertion in a streaming
// handler would fail and event streaming would be silently unavailable.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
