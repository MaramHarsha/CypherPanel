package sharedvars

import (
	"errors"
	"strings"
	"testing"
)

// The grammar is the contract both callers share (shared-variables.md §3), so
// it gets the densest table in the feature: the writer decides what is stored
// and the resolver decides what ships, and a disagreement between them is
// exactly the class of bug this pair exists to prevent.

func TestRefs(t *testing.T) {
	cases := map[string]struct {
		value string
		want  []string
	}{
		"no reference":         {"plain value", nil},
		"whole value":          {"{{shared.SENTRY_DSN}}", []string{"SENTRY_DSN"}},
		"substring":            {"prefix-{{shared.A}}-suffix", []string{"A"}},
		"several, composed":    {"postgres://{{shared.DB_USER}}:{{shared.DB_PASS}}@db:5432/app", []string{"DB_USER", "DB_PASS"}},
		"repeat is one ref":    {"{{shared.A}} and {{shared.A}}", []string{"A"}},
		"underscore lead":      {"{{shared._X9}}", []string{"_X9"}},
		"closing brace alone":  {"a }} b", nil},
		"literal braces apart": {"{ {shared.A} }", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Refs(tc.value)
			if err != nil {
				t.Fatalf("Refs(%q) = %v", tc.value, err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Refs(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// Every one of these is a 400 at write time. Coolify ships the literal
// `{{project.FOO}}` into the container instead; that is the behaviour §3 says
// we pointedly do not port.
func TestRefsRejectsMalformed(t *testing.T) {
	for _, value := range []string{
		"{{ shared.A }}",
		"{{shared. A}}",
		"{{shared.}}",
		"{{shared.9A}}",
		"{{shared.A-B}}",
		"{{project.A}}",
		"{{environment.A}}",
		"{{A}}",
		"unterminated {{shared.A",
		"{{{{shared.A}}}}",
	} {
		if _, err := Refs(value); !errors.Is(err, ErrMalformedReference) {
			t.Errorf("Refs(%q) = %v, want ErrMalformedReference", value, err)
		}
	}
}

func TestRefsBoundsCount(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxReferences; i++ {
		b.WriteString("{{shared.K")
		b.WriteByte(byte('A' + i))
		b.WriteString("}}")
	}
	if got, err := Refs(b.String()); err != nil || len(got) != MaxReferences {
		t.Fatalf("Refs at the limit = %d refs, %v; want %d, nil", len(got), err, MaxReferences)
	}
	b.WriteString("{{shared.OVER}}")
	if _, err := Refs(b.String()); !errors.Is(err, ErrTooManyReferences) {
		t.Fatalf("Refs past the limit = %v, want ErrTooManyReferences", err)
	}
}

// A malformed reference must never echo the value into an error: the value is
// about to be sealed, and error messages are one of the places secrets must
// never appear (ENGINEERING rule 20).
func TestMalformedErrorDoesNotEchoTheValue(t *testing.T) {
	_, err := Refs("hunter2-{{ shared.A }}-hunter2")
	if err == nil {
		t.Fatal("Refs accepted a malformed reference")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("the value leaked into the error: %q", err)
	}
}

func TestExpand(t *testing.T) {
	vals := map[string]string{"A": "1", "B": "2", "EMPTY": ""}
	cases := map[string]struct{ in, want string }{
		"whole value": {"{{shared.A}}", "1"},
		"substring":   {"x{{shared.A}}y", "x1y"},
		"several":     {"postgres://{{shared.A}}:{{shared.B}}@db", "postgres://1:2@db"},
		"repeat":      {"{{shared.A}}{{shared.A}}", "11"},
		"none":        {"plain", "plain"},
		// An empty shared value is a defined value, not a missing one.
		"defined empty": {"[{{shared.EMPTY}}]", "[]"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Expand(tc.in, vals)
			if err != nil || got != tc.want {
				t.Fatalf("Expand(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
			}
		})
	}
}

// One level, always: a shared value that happens to contain a reference is
// substituted verbatim and never rescanned, so there is no recursion, no cycle
// and no expansion order to define (§3). (The service refuses to STORE such a
// value; this proves the resolver is safe even if one ever existed.)
func TestExpandDoesNotRecurse(t *testing.T) {
	got, err := Expand("{{shared.A}}", map[string]string{"A": "{{shared.B}}", "B": "boom"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "{{shared.B}}" {
		t.Fatalf("Expand = %q, want the substituted text left verbatim", got)
	}
}

// The failure that matters: an unresolvable reference is an error naming the
// key, never a silent empty string (§4).
func TestExpandMissingKeyFailsNamingIt(t *testing.T) {
	_, err := Expand("x{{shared.NOPE}}y", map[string]string{"A": "1"})
	var missing *MissingReferenceError
	if !errors.As(err, &missing) {
		t.Fatalf("Expand = %v, want MissingReferenceError", err)
	}
	if missing.Key != "NOPE" {
		t.Fatalf("missing key = %q, want NOPE", missing.Key)
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"A", "_A", "A1", "SENTRY_DSN"} {
		if !ValidKey(k) {
			t.Errorf("ValidKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"", "1A", "A-B", "A B", "A=B", "A\nB"} {
		if ValidKey(k) {
			t.Errorf("ValidKey(%q) = true, want false", k)
		}
	}
}

func TestContainsReference(t *testing.T) {
	if !ContainsReference("a{{shared.X}}b") || !ContainsReference("{{shared.}}") {
		t.Error("ContainsReference missed a nested reference")
	}
	if ContainsReference("{{project.X}}") || ContainsReference("plain") {
		t.Error("ContainsReference matched something that is not a shared reference")
	}
}
