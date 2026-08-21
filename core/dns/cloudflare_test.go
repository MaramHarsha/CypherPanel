package dns

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
			c := &cloudflare{token: "t", base: srv.URL, http: srv.Client()}

			_, err := c.ListZones(context.Background())
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
	c := &cloudflare{token: "t", base: srv.URL, http: srv.Client()}

	_, err := c.ListZones(context.Background())
	var ae *AuthError
	if errors.As(err, &ae) {
		t.Fatalf("a server-side failure was reported as a credential problem: %v", err)
	}
	if err == nil {
		t.Fatal("a 500 was treated as success")
	}
}
