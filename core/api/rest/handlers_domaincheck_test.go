package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A domain that resolves and answers is not the same as a domain that reaches
// your app — the case that made a deploy look successful while a completely
// different program served the page.
func TestCheckDomainDistinguishesWhoAnswers(t *testing.T) {
	t.Run("no domain configured", func(t *testing.T) {
		got := checkDomain(context.Background(), "", false)
		if got.Verdict != verdictNoDomain {
			t.Errorf("verdict = %q, want %q", got.Verdict, verdictNoDomain)
		}
		if got.Remedy == "" {
			t.Error("every non-ok verdict must offer a remedy (ui-principles §11)")
		}
	})

	t.Run("does not resolve", func(t *testing.T) {
		got := checkDomain(context.Background(), "definitely-not-a-real-domain.invalid", false)
		if got.Verdict != verdictNoDNS {
			t.Errorf("verdict = %q, want %q", got.Verdict, verdictNoDNS)
		}
		if !strings.Contains(got.Remedy, "A record") {
			t.Errorf("remedy should name the DNS record to create, got %q", got.Remedy)
		}
	})
}

// The marker is the whole point: without it, our proxy and a stranger's web
// server are indistinguishable from the outside.
func TestCheckDomainReadsTheProxyMarker(t *testing.T) {
	cases := map[string]struct {
		header string
		want   string
	}{
		"our proxy answered": {servedByValue, verdictOK},
		"something else did": {"", verdictForeign},
		"a different marker": {"someone-else", verdictForeign},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set(servedByHeader, tc.header)
				}
				w.Header().Set("Server", "nginx/1.24.0")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			u, _ := url.Parse(srv.URL)
			got := checkDomain(context.Background(), u.Host, false)
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %q (%s), want %q", got.Verdict, got.Summary, tc.want)
			}
			if tc.want == verdictForeign {
				// The operator needs to know WHAT answered, or the report is
				// just "something is wrong".
				if !strings.Contains(got.Summary, "nginx") {
					t.Errorf("summary should name what answered, got %q", got.Summary)
				}
				if got.Remedy == "" {
					t.Error("a foreign answer must come with a remedy")
				}
			}
		})
	}
}
