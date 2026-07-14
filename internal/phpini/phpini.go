// Package phpini defines the bounded, validated set of php.ini directives an
// account may override (MultiPHP INI editor, plan.md §4B). This is an
// allowlist — never arbitrary INI injection — and values are applied as
// pool-level php_admin_value so accounts cannot exceed hard limits.
package phpini

import (
	"fmt"
	"regexp"
)

// allowed is the directive allowlist. Extend deliberately; every key here is
// safe to expose per-account and enforced at the pool level.
var allowed = map[string]bool{
	"memory_limit":        true,
	"upload_max_filesize": true,
	"post_max_size":       true,
	"max_execution_time":  true,
	"max_input_time":      true,
	"max_input_vars":      true,
	"display_errors":      true,
}

// AllowedKeys returns the sorted allowlist (stable order for UIs/tests).
func AllowedKeys() []string {
	keys := make([]string, 0, len(allowed))
	for k := range allowed {
		keys = append(keys, k)
	}
	// small fixed set; simple insertion into a deterministic order
	order := []string{
		"memory_limit", "upload_max_filesize", "post_max_size",
		"max_execution_time", "max_input_time", "max_input_vars", "display_errors",
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		if allowed[k] {
			out = append(out, k)
		}
	}
	return out
}

// valueRe keeps values to safe php.ini tokens: sizes (512M), integers, and
// on/off — no whitespace, newlines, or characters that could break out of the
// directive line.
var valueRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

// Validate checks a settings map against the allowlist and value format.
// Returns a cleaned copy (unknown keys are rejected, not silently dropped).
func Validate(settings map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(settings))
	for k, v := range settings {
		if !allowed[k] {
			return nil, fmt.Errorf("phpini: directive %q is not editable", k)
		}
		if v == "" {
			continue // empty means "unset / use default"
		}
		if !valueRe.MatchString(v) {
			return nil, fmt.Errorf("phpini: invalid value %q for %q", v, k)
		}
		out[k] = v
	}
	return out, nil
}
