package databases

// Bounds on operator-supplied resource numbers (validateLimits): they are
// persisted as int32 and travel as uint32 on DbSpec, so unchecked values would
// wrap on the cast (CWE-190). Exercised directly — Create and Update both call
// validateLimits before any narrowing.

import (
	"math"
	"strings"
	"testing"
)

func ptrF(f float64) *float64 { return &f }
func ptrI(i int) *int         { return &i }

func TestValidateLimitsBounds(t *testing.T) {
	cases := map[string]struct {
		cpu  *float64
		mem  *int
		port *int
		ok   bool
	}{
		"all nil":              {nil, nil, nil, true},
		"all zero (clear)":     {ptrF(0), ptrI(0), ptrI(0), true},
		"sane values":          {ptrF(1.5), ptrI(2048), ptrI(5432), true},
		"port upper bound":     {nil, nil, ptrI(65535), true},
		"mem upper bound":      {nil, ptrI(math.MaxInt32), nil, true},
		"negative cpu":         {ptrF(-1), nil, nil, false},
		"NaN cpu":              {ptrF(math.NaN()), nil, nil, false},
		"Inf cpu":              {ptrF(math.Inf(1)), nil, nil, false},
		"negative mem":         {nil, ptrI(-1), nil, false},
		"overflowing mem":      {nil, ptrI(math.MaxInt32 + 1), nil, false},
		"negative port":        {nil, nil, ptrI(-1), false},
		"port above 65535":     {nil, nil, ptrI(65536), false},
		"port wraps to uint32": {nil, nil, ptrI(-2), false}, // would become ~4.29e9 on the wire
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateLimits(c.cpu, c.mem, c.port)
			if c.ok && err != nil {
				t.Fatalf("validateLimits = %v, want nil", err)
			}
			if !c.ok && err == nil {
				t.Fatal("validateLimits = nil, want ValidationError")
			}
		})
	}
}

// The initial database name reaches the engine image's own entrypoint, so it
// is bounded to a plain SQL identifier — one that cannot depend on that
// entrypoint's quoting, and that is legal on every engine that has one
// (managed-databases.md §2).
func TestInitialDatabaseValidation(t *testing.T) {
	cases := map[string]struct {
		engine, name string
		ok           bool
	}{
		"omitted":                 {"postgresql", "", true},
		"plain":                   {"postgresql", "appdb", true},
		"underscored":             {"mysql", "my_app_db", true},
		"leading underscore":      {"mariadb", "_internal", true},
		"digits after a letter":   {"mysql", "app2", true},
		"mongo names a database":  {"mongodb", "app", true},
		"whitespace is trimmed":   {"postgresql", "  appdb  ", true},
		"63 characters":           {"postgresql", "a" + repeat("b", 62), true},
		"64 characters":           {"postgresql", "a" + repeat("b", 63), false},
		"leading digit":           {"postgresql", "2app", false},
		"hyphen":                  {"postgresql", "my-app", false},
		"quote":                   {"postgresql", `app"; DROP DATABASE x; --`, false},
		"backtick":                {"mysql", "app`db", false},
		"space inside":            {"postgresql", "my app", false},
		"redis has no named dbs":  {"redis", "appdb", false},
		"valkey has no named dbs": {"valkey", "appdb", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := CreateInput{Name: "db", Engine: c.engine, ServerID: "srv_1", InitialDatabase: c.name}
			got, err := validateAndDefault(in)
			if c.ok && err != nil {
				t.Fatalf("validateAndDefault = %v, want nil", err)
			}
			if !c.ok {
				if err == nil {
					t.Fatal("validateAndDefault = nil, want ValidationError")
				}
				return
			}
			if want := strings.TrimSpace(c.name); got.InitialDatabase != want {
				t.Fatalf("InitialDatabase = %q, want %q", got.InitialDatabase, want)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
