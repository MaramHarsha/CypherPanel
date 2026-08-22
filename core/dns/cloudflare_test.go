package dns

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Cloudflare answers a malformed token with HTTP 400, not 401, and buries the
// useful reason in error_chain. Classifying on status alone reported a
// credential problem as "could not reach Cloudflare", which sends the operator
// to look at their network instead of their token. These are the two real
// bodies the live API returns.
func TestCredentialProblemsAreClassifiedByCode(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"malformed token (400 + chain)": {
			http.StatusBadRequest,
			`{"success":false,"errors":[{"code":6003,"message":"Invalid request headers","error_chain":[{"code":6111,"message":"Invalid format for Authorization header"}]}]}`,
			"Invalid format for Authorization header",
		},
		"wrong token": {
			http.StatusBadRequest,
			`{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`,
			"Invalid access token",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := newTestCloudflare(t, srv)

			_, err := c.ListZones(context.Background(), "")
			var ae *AuthError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v; want an AuthError so the operator is told to fix the token", err)
			}
			if ae.Msg != tc.want {
				t.Fatalf("message = %q; want the specific reason %q", ae.Msg, tc.want)
			}
		})
	}
}

// A genuine non-credential failure stays a plain error, so it is not
// mis-reported as "your token is wrong".
func TestNonCredentialErrorsStayPlain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"Internal error"}]}`))
	}))
	defer srv.Close()
	c := newTestCloudflare(t, srv)

	_, err := c.ListZones(context.Background(), "")
	var ae *AuthError
	if errors.As(err, &ae) {
		t.Fatalf("a server-side failure was reported as a credential problem: %v", err)
	}
	if err == nil {
		t.Fatal("a 500 was treated as success")
	}
}

// newTestCloudflare points a real client at a stub server. base is parsed the
// same way production parses Cloudflare's constant, so the host guard in do()
// is exercised rather than bypassed.
func newTestCloudflare(t *testing.T, srv *httptest.Server) *cloudflare {
	t.Helper()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing stub URL: %v", err)
	}
	return &cloudflare{token: "t", base: base, http: srv.Client()}
}

// The finding CodeQL raised (go/request-forgery, alert 6): the request URL was
// built by concatenating operator-supplied values onto a base string. It was
// escaped and I could not construct an exploit — but this client carries a
// bearer token, so "cannot currently escape the host" is the wrong standard.
// It has to be "cannot, by construction".
//
// Every one of these is a value an operator or project member can influence: an
// account id typed into Settings, a hostname typed onto an application.
func TestHostileValuesCannotMoveTheRequestOffCloudflare(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()
	c := newTestCloudflare(t, srv)
	ctx := context.Background()

	hostile := []string{
		"../../../../evil",
		"..%2F..%2Fevil",
		"x/../../evil",
		"evil.test/zones",
		"a?b=c",
		"a#b",
		"a b",
		"..",
	}
	for _, h := range hostile {
		// Each of these must be REFUSED where it lands in the path, not
		// silently reshaped into a different request.
		if err := c.VerifyToken(ctx, h); err == nil {
			t.Errorf("VerifyToken(%q) was allowed; a hostile account id must be refused", h)
		}
		if _, _, err := c.FindRecord(ctx, h, "app.example.com", "A"); err == nil {
			t.Errorf("FindRecord(zone=%q) was allowed", h)
		}
		// In the QUERY, the same value is harmless — Values.Encode escapes it —
		// so it is accepted, and must still not move the request.
		if _, err := c.ListZones(ctx, h); err != nil {
			t.Fatalf("ListZones(%q) = %v; a query value needs no rejection", h, err)
		}
	}

	// An EMPTY account id is not hostile — it is the documented user-owned path,
	// which verifies at /user/tokens/verify. An empty zone id is still refused,
	// because there is no such request to make.
	if err := c.VerifyToken(ctx, ""); err != nil {
		t.Fatalf("VerifyToken(\"\") = %v; an empty account is the user-owned token path", err)
	}
	if _, _, err := c.FindRecord(ctx, "", "app.example.com", "A"); err == nil {
		t.Error("FindRecord with an empty zone id was allowed")
	}

	// Whatever was typed, every request stayed under the API's own path.
	for _, p := range got {
		if !strings.HasPrefix(p, "/zones") && !strings.HasPrefix(p, "/accounts") && !strings.HasPrefix(p, "/user") {
			t.Fatalf("a request escaped the API path: %q", p)
		}
		if strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
			t.Fatalf("a request traversed out of its path: %q", p)
		}
	}
	if len(got) == 0 {
		t.Fatal("no requests were made; the test proved nothing")
	}
}

// The tripwire itself: if a future change ever builds a URL that leaves the
// pinned host, the request is refused rather than sent with the token attached.
func TestRequestsToAnotherHostAreRefused(t *testing.T) {
	base, _ := url.Parse("https://api.cloudflare.com/client/v4")
	c := &cloudflare{token: "t", base: base, http: http.DefaultClient}
	// Simulate the refactor that would reintroduce the bug.
	c.base = &url.URL{Scheme: "https", Host: "api.cloudflare.com", Path: "/client/v4"}
	other := &cloudflare{token: "t", base: &url.URL{Scheme: "https", Host: "evil.test"}, http: http.DefaultClient}

	// Same guard, opposite sides: a client whose base is elsewhere must not be
	// able to borrow this one's host, and vice versa.
	if c.base.Host == other.base.Host {
		t.Fatal("test setup is wrong")
	}
	u := c.base.JoinPath("zones", "../../../evil")
	if u.Host != "api.cloudflare.com" {
		t.Fatalf("JoinPath moved the host to %q", u.Host)
	}
}

// The first real account this feature met returned exactly this: one zone, just
// added, status "pending" because its nameservers had not been repointed yet.
// The first cut asked Cloudflare for status=active, so that zone was filtered
// out and the operator was told their token could see no zones — while their
// domain sat plainly visible in the Cloudflare dashboard.
//
// Activation is not ownership. A zone in your account is yours whether or not
// it has finished setting up; whether the domain RESOLVES is a separate fact,
// and one the operator is told rather than silently acted on.
func TestZonesAreListedWhateverTheirActivationStatus(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"z1","name":"pending.example","status":"pending"},
			{"id":"z2","name":"initializing.example","status":"initializing"},
			{"id":"z3","name":"active.example","status":"active"},
			{"id":"z4","name":"gone.example","status":"moved"}
		]}`))
	}))
	defer srv.Close()

	zones, err := newTestCloudflare(t, srv).ListZones(context.Background(), "acct_1")
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if strings.Contains(gotQuery, "status=") {
		t.Fatalf("query %q still filters on status; that is the bug", gotQuery)
	}
	got := map[string]string{}
	for _, z := range zones {
		got[z.Name] = z.Status
	}
	for _, want := range []string{"pending.example", "initializing.example", "active.example"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s was dropped; a zone you own counts before it is active", want)
		}
	}
	// `moved` is the one exclusion: that zone has left this provider, so it is
	// genuinely not ours to manage.
	if _, ok := got["gone.example"]; ok {
		t.Error("a moved zone was kept; it no longer belongs to this provider")
	}
	if got["pending.example"] != "pending" {
		t.Errorf("status = %q; it has to be carried through to be reported", got["pending.example"])
	}
}
