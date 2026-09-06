package rest

// Tests for the request-id, client-address and recovery middleware
// (control-plane-hardening.md §2, §5). The property they exist for: any
// response an operator can screenshot carries an id that finds the request in
// the panel's log, and no client can choose that id or its own rate-limit key.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/logring"
)

// panickingPinger makes GET /readyz — a real route, through the real
// middleware chain — blow up, which is how the recovery path is exercised
// without a handler that exists only for tests.
type panickingPinger struct{}

func (panickingPinger) Ping(context.Context) error { panic("boom: the store handle is nil") }

var traceIDPattern = regexp.MustCompile(`^trace_[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$`)

// newMiddlewareServer builds an API with the given trusted proxies and pinger,
// logging into ring so a test can read back what the request produced.
func newMiddlewareServer(t *testing.T, proxies []netip.Prefix, pinger Pinger, ring *logring.Ring) *httptest.Server {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	var handler slog.Handler = slog.NewTextHandler(io.Discard, nil)
	if ring != nil {
		handler = ring.Handler(&slog.HandlerOptions{Level: slog.LevelInfo})
	}
	api := New(Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(2, time.Minute), time.Hour),
		Teams:          newFakeTeams(),
		Pinger:         pinger,
		CACertPEM:      []byte("x"),
		TrustedProxies: proxies,
		Log:            slog.New(handler),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestEveryResponseCarriesATraceID: the header is on success, on a refusal and
// on a fault, and the body of every error repeats it (canvas 13s).
func TestEveryResponseCarriesATraceID(t *testing.T) {
	ts := newMiddlewareServer(t, nil, okPinger{}, nil)
	token := login(t, ts)

	for _, tc := range []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
		hasBody    bool
	}{
		{"health", "GET", "/healthz", "", http.StatusOK, false},
		{"authenticated read", "GET", "/api/v1/auth/me", token, http.StatusOK, false},
		{"unauthenticated", "GET", "/api/v1/auth/me", "", http.StatusUnauthorized, true},
		{"unknown api path", "GET", "/api/v1/nope", token, http.StatusNotFound, true},
	} {
		status, header, body := doJSON(t, tc.method, ts.URL+tc.path, tc.token, "")
		if status != tc.wantStatus {
			t.Errorf("%s = %d, want %d (body %s)", tc.name, status, tc.wantStatus, body)
		}
		id := header.Get(TraceIDHeader)
		if !traceIDPattern.MatchString(id) {
			t.Errorf("%s: %s = %q, want a trace_xxxx-xxxx-xxxx-xxxx id", tc.name, TraceIDHeader, id)
		}
		if !tc.hasBody {
			continue
		}
		var e errorBody
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", tc.name, body, err)
		}
		if e.TraceID != id {
			t.Errorf("%s: body trace_id = %q, header = %q — an operator pasting one must find the other", tc.name, e.TraceID, id)
		}
		if e.Error == "" {
			t.Errorf("%s: the envelope carries no message", tc.name)
		}
	}

	// Two requests never share an id.
	_, h1, _ := doJSON(t, "GET", ts.URL+"/healthz", "", "")
	_, h2, _ := doJSON(t, "GET", ts.URL+"/healthz", "", "")
	if h1.Get(TraceIDHeader) == h2.Get(TraceIDHeader) {
		t.Error("two requests were given the same trace id")
	}
}

// TestPanicBecomesTheErrorEnvelopeWithATraceID: a handler panic is a 500 with
// the ordinary envelope — not a bare string, not a dropped connection — and the
// log carries the same id at error level, so the id an operator pastes finds
// the fault (§2 acceptance 3).
func TestPanicBecomesTheErrorEnvelopeWithATraceID(t *testing.T) {
	ring := logring.New(50)
	ts := newMiddlewareServer(t, nil, panickingPinger{}, ring)

	status, header, body := doJSON(t, "GET", ts.URL+"/readyz", "", "")
	if status != http.StatusInternalServerError {
		t.Fatalf("a panicking handler = %d, want 500 (body %s)", status, body)
	}
	id := header.Get(TraceIDHeader)
	if !traceIDPattern.MatchString(id) {
		t.Fatalf("%s = %q on a 500", TraceIDHeader, id)
	}
	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("a panic did not produce the Error envelope: %s (%v)", body, err)
	}
	if e.Error == "" || e.TraceID != id {
		t.Fatalf("envelope = %+v, want a message and trace_id %q", e, id)
	}
	if strings.Contains(string(body), "boom: the store handle is nil") {
		t.Error("the panic value leaked to the client")
	}

	lines := strings.Join(ring.Tail(50), "\n")
	if !strings.Contains(lines, "level=ERROR") || !strings.Contains(lines, "panic in handler") {
		t.Fatalf("the panic was not logged at error level:\n%s", lines)
	}
	if !strings.Contains(lines, "trace_id="+id) {
		t.Fatalf("no log line carries trace_id=%s:\n%s", id, lines)
	}
	// The request line itself is an error too, and names the route it matched.
	if !strings.Contains(lines, `msg="http request failed"`) || !strings.Contains(lines, `route="GET /readyz"`) {
		t.Fatalf("the 5xx request line is missing or does not name the route:\n%s", lines)
	}
}

// TestFiveHundredsAreLoggedAtErrorLevel: an ordinary (non-panic) 5xx is an
// error line too — a fault the operator never saw is the one that costs hours.
func TestFiveHundredsAreLoggedAtErrorLevel(t *testing.T) {
	ring := logring.New(50)
	// No Teams service configured: the authz helper fails closed with 500.
	hash, _ := auth.HashPassword(testPassword)
	api := New(Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleMember},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Pinger:    okPinger{},
		CACertPEM: []byte("x"),
		Log:       slog.New(ring.Handler(&slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	token := login(t, ts)

	status, header, _ := doJSON(t, "GET", ts.URL+"/api/v1/projects/prj_x/notifiers", token, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 from the fail-closed authorization path", status)
	}
	lines := strings.Join(ring.Tail(50), "\n")
	if !strings.Contains(lines, `level=ERROR msg="http request failed"`) {
		t.Fatalf("a 500 was logged below error level:\n%s", lines)
	}
	if !strings.Contains(lines, "trace_id="+header.Get(TraceIDHeader)) {
		t.Fatalf("the 500 request line carries no trace id:\n%s", lines)
	}
	// A 200 stays at info: an error-level line per request would drown the log.
	if !strings.Contains(lines, `level=INFO msg="http request"`) {
		t.Fatalf("successful requests are no longer logged at info:\n%s", lines)
	}
}

// TestInboundTraceIDIsHonouredOnlyFromATrustedProxy: with no trusted proxies —
// the default — a client-chosen id is discarded, so nobody can file their
// requests under someone else's id or inject into a log line.
func TestInboundTraceIDIsHonouredOnlyFromATrustedProxy(t *testing.T) {
	untrusted := newMiddlewareServer(t, nil, okPinger{}, nil)
	given := "trace_client-chosen"
	_, header, _ := doWithHeaders(t, untrusted.URL+"/healthz", map[string]string{TraceIDHeader: given})
	if got := header.Get(TraceIDHeader); got == given {
		t.Fatal("a client-supplied request id was honoured from an untrusted peer")
	} else if !traceIDPattern.MatchString(got) {
		t.Fatalf("%s = %q, want a freshly minted id", TraceIDHeader, got)
	}

	// httptest serves on loopback, so trusting loopback is "the peer is our
	// proxy" for this test.
	trusted := newMiddlewareServer(t, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}, okPinger{}, nil)
	_, header, _ = doWithHeaders(t, trusted.URL+"/healthz", map[string]string{TraceIDHeader: given})
	if got := header.Get(TraceIDHeader); got != given {
		t.Fatalf("%s = %q from a trusted proxy, want the supplied %q", TraceIDHeader, got, given)
	}
	// Even a trusted proxy cannot pass an id that would corrupt a log line.
	// (A literal newline is rejected by net/http before it reaches us, so the
	// charset itself is asserted in TestValidTraceIDAcceptsOnlyPlainTokens.)
	for _, bad := range []string{
		"has spaces", strings.Repeat("x", traceIDMaxLen+1), "quote\"d", "",
	} {
		_, header, _ = doWithHeaders(t, trusted.URL+"/healthz", map[string]string{TraceIDHeader: bad})
		if got := header.Get(TraceIDHeader); got == bad || !traceIDPattern.MatchString(got) {
			t.Errorf("id %q was accepted (answered %q); only short printable tokens may be reused", bad, got)
		}
	}
}

// TestClientIPHonoursForwardedHeadersOnlyFromATrustedProxy: the rate-limit key
// is the TCP peer unless the peer is a configured proxy, and then it is the
// right-most forwarded address that is not itself a trusted hop — so a client
// cannot pick its own key by prepending addresses (§5).
func TestClientIPHonoursForwardedHeadersOnlyFromATrustedProxy(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	api := New(Deps{TrustedProxies: proxies, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})

	for _, tc := range []struct {
		name string
		peer string
		xff  []string
		real string
		want string
	}{
		{"untrusted peer, headers ignored", "203.0.113.7:5555", []string{"1.2.3.4"}, "5.6.7.8", "203.0.113.7"},
		{"untrusted peer, no headers", "203.0.113.7:5555", nil, "", "203.0.113.7"},
		{"trusted peer, single hop", "10.0.0.1:5555", []string{"198.51.100.9"}, "", "198.51.100.9"},
		{"trusted peer, chained proxies", "10.0.0.1:5555", []string{"198.51.100.9, 10.0.0.2", "10.0.0.3"}, "", "198.51.100.9"},
		{"trusted peer, client forged a hop", "10.0.0.1:5555", []string{"10.9.9.9, 198.51.100.9"}, "", "198.51.100.9"},
		{"trusted peer, x-real-ip fallback", "10.0.0.1:5555", nil, "198.51.100.9", "198.51.100.9"},
		{"trusted peer, garbage forwarded", "10.0.0.1:5555", []string{"not-an-ip"}, "", "10.0.0.1"},
		{"trusted peer, only trusted hops", "10.0.0.1:5555", []string{"10.0.0.2, 10.0.0.3"}, "", "10.0.0.1"},
		{"forwarded entry with a port", "10.0.0.1:5555", []string{"198.51.100.9:41234"}, "", "198.51.100.9"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
		r.RemoteAddr = tc.peer
		for _, v := range tc.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if tc.real != "" {
			r.Header.Set("X-Real-IP", tc.real)
		}
		if got := api.clientIP(r); got != tc.want {
			t.Errorf("%s: clientIP = %q, want %q", tc.name, got, tc.want)
		}
	}

	// With nothing configured, no header can ever speak for a client.
	bare := New(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := bare.clientIP(r); got != "10.0.0.1" {
		t.Errorf("with no trusted proxies clientIP = %q, want the peer", got)
	}
}

// TestLoginThrottleAnswersWithACountdown: 429 carries Retry-After and the same
// number in the body, so the sign-in screen can count down instead of guessing
// (canvas 13t).
func TestLoginThrottleAnswersWithACountdown(t *testing.T) {
	ts := newMiddlewareServer(t, nil, okPinger{}, nil)
	body := `{"email":"` + testEmail + `","password":"wrong"}`
	for range 2 {
		if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "", body); status != http.StatusUnauthorized {
			t.Fatalf("a wrong password = %d, want 401", status)
		}
	}
	status, header, respBody := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "", body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("the third attempt = %d, want 429 (body %s)", status, respBody)
	}
	retryAfter := header.Get("Retry-After")
	secs, err := strconv.Atoi(retryAfter)
	if err != nil || secs < 1 || secs > 60 {
		t.Fatalf("Retry-After = %q, want 1..60 seconds", retryAfter)
	}
	var e errorBody
	if err := json.Unmarshal(respBody, &e); err != nil {
		t.Fatalf("unmarshal %s: %v", respBody, err)
	}
	if e.RetryAfterSeconds != secs {
		t.Fatalf("retry_after_seconds = %d, Retry-After = %d; they must agree", e.RetryAfterSeconds, secs)
	}
	if e.TraceID != header.Get(TraceIDHeader) {
		t.Fatalf("the 429 body carries trace_id %q, header %q", e.TraceID, header.Get(TraceIDHeader))
	}
	// The correct password from the same address is refused too — the throttle
	// is on the attempt, not on the outcome.
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/login", "",
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`); status != http.StatusTooManyRequests {
		t.Fatalf("a correct password from a throttled address = %d, want 429", status)
	}
}

// doWithHeaders issues a GET with arbitrary request headers.
func doWithHeaders(t *testing.T, url string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, resp.Header, data
}

// TestValidTraceIDAcceptsOnlyPlainTokens: an id reaches every log line for the
// request, so anything that could break a line — or bloat it — is refused and
// replaced with a minted one (§2).
func TestValidTraceIDAcceptsOnlyPlainTokens(t *testing.T) {
	for _, ok := range []string{
		"trace_9f3a-11bd-4c02-7e51", "abc123", "a.b:c-d_e", strings.Repeat("x", traceIDMaxLen),
	} {
		if !validTraceID(ok) {
			t.Errorf("validTraceID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"", " ", "has spaces", "line\nbreak", "carriage\rreturn", "tab\there",
		"quote\"d", "semi;colon", "slash/es", "unicode—dash", "null\x00byte",
		strings.Repeat("x", traceIDMaxLen+1),
	} {
		if validTraceID(bad) {
			t.Errorf("validTraceID(%q) = true, want false", bad)
		}
	}
}

// TestNewTraceIDIsUniqueAndWellShaped: the shape the UI renders, and no
// collisions across a batch.
func TestNewTraceIDIsUniqueAndWellShaped(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := newTraceID()
		if !traceIDPattern.MatchString(id) {
			t.Fatalf("newTraceID = %q, want trace_xxxx-xxxx-xxxx-xxxx", id)
		}
		if seen[id] {
			t.Fatalf("newTraceID repeated %q", id)
		}
		seen[id] = true
	}
}

// An invitation's wire token travels in the URL — the two public routes take it
// as a path segment, and the mailed link opens the SPA route this same binary
// serves. The panel's own log must not keep the secret half: `GET /panel/logs`
// hands that log to a team owner, and a reverse proxy copies every request line
// besides (invitations-and-access-requests.md §8, threat-model §5.8).
func TestRequestLogRedactsInvitationSecrets(t *testing.T) {
	const secret = "WDJULIAJZY3WXIRIYEP55DMTWK"
	for _, tc := range []struct{ name, path, want string }{
		{"preview", "/api/v1/invites/inv_abc." + secret, "/api/v1/invites/inv_abc.…"},
		{"accept", "/api/v1/invites/inv_abc." + secret + "/accept", "/api/v1/invites/inv_abc.…/accept"},
		{"the mailed link's SPA route", "/invite/inv_abc." + secret, "/invite/inv_abc.…"},
		// A malformed token carries no secret to hide, and seeing it is what
		// makes a bad link diagnosable.
		{"no secret half", "/api/v1/invites/inv_abc", "/api/v1/invites/inv_abc"},
		// Everything else is untouched: this is a rule about one credential,
		// not a general scrubber that would quietly hide the next one.
		{"an ordinary path", "/api/v1/applications/app_x/env/API_KEY", "/api/v1/applications/app_x/env/API_KEY"},
		{"a path that merely mentions invites", "/api/v1/teams/tm_1/invites", "/api/v1/teams/tm_1/invites"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactPath(tc.path); got != tc.want {
				t.Fatalf("redactPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
			if strings.Contains(redactPath(tc.path), secret) {
				t.Fatalf("the secret survived redaction: %q", redactPath(tc.path))
			}
		})
	}
}

// A crash inside the invitation routes must not become the one place a live
// token lands in the log: the panic line is redacted like the request line.
func TestPanicLogRedactsInvitationSecrets(t *testing.T) {
	const secret = "M7JTDRIWDKNC43RSU6LUYEX6IH"
	var buf strings.Builder
	api := New(Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))})
	h := api.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/invites/inv_abc."+secret, nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("the panic log kept an invitation secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "inv_abc") {
		t.Fatalf("the panic log lost the public invite id, which is what makes it correlatable: %s", buf.String())
	}
}
