package quota

import (
	"reflect"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Warn at 90, refuse at 100, and UNCAPPED is always ok — a dimension with no
// cap cannot be exceeded, and drawing it amber would train the operator to
// ignore the colour.
func TestQuotaStateThresholds(t *testing.T) {
	limit := func(n int64) *int64 { return &n }
	cases := []struct {
		used  int64
		limit *int64
		want  string
	}{
		{0, nil, domain.QuotaOK},
		{1 << 40, nil, domain.QuotaOK},
		{0, limit(100), domain.QuotaOK},
		{89, limit(100), domain.QuotaOK},
		{90, limit(100), domain.QuotaWarn},
		{99, limit(100), domain.QuotaWarn},
		{100, limit(100), domain.QuotaExceeded},
		{101, limit(100), domain.QuotaExceeded},
	}
	for _, c := range cases {
		if got := domain.QuotaState(c.used, c.limit); got != c.want {
			t.Errorf("QuotaState(%d, %v) = %q, want %q", c.used, c.limit, got, c.want)
		}
	}
}

// A quota is denominated in bytes and counts. This test exists so a future
// change that adds a monetary field to the row has to delete an assertion that
// says why it must not — ADR-012 rule 1, made mechanical.
func TestNoMonetaryConceptExistsOnAQuota(t *testing.T) {
	forbidden := []string{"price", "cost", "rate", "currency", "plan", "tier", "invoice", "amount"}
	fields := reflectFieldNames(domain.ResourceQuota{})
	for _, f := range fields {
		lower := lowerASCII(f)
		for _, bad := range forbidden {
			if contains(lower, bad) {
				t.Errorf("ResourceQuota has a field %q, which looks monetary. ADR-012 permits quotas precisely because they are not: adding one is a change to the vision's out-of-scope list and needs its own recorded decision.", f)
			}
		}
	}
}

func reflectFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
