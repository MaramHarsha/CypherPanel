package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunRejectsUnknownCommands(t *testing.T) {
	err := run(context.Background(), []string{"nonsense"})
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error should name the problem, got: %v", err)
	}
}

func TestRunRejectsUnknownSubcommands(t *testing.T) {
	err := run(context.Background(), []string{"accounts", "nonsense"})
	if err == nil {
		t.Fatal("expected an error for an unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown accounts subcommand") {
		t.Errorf("error should name the group, got: %v", err)
	}
}

func TestRunRequiresASubcommand(t *testing.T) {
	if err := run(context.Background(), []string{"backup"}); err == nil {
		t.Fatal("a bare group should not be runnable")
	}
}

func TestHelpAndVersionSucceed(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"help"}, {"version"}} {
		if err := run(context.Background(), args); err != nil {
			t.Errorf("run(%v) = %v, want nil", args, err)
		}
	}
}

// Terminating destroys an account's Linux user and data. It must never happen
// from a single flag — the guard has to fire before any network call, so a
// missing --yes fails here rather than after contacting the API.
func TestTerminateRequiresExplicitConfirmation(t *testing.T) {
	err := run(context.Background(), []string{"accounts", "terminate", "-id", "abc"})
	if err == nil {
		t.Fatal("terminate without --yes should be refused")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should point at the confirmation flag, got: %v", err)
	}
}

// Same discipline for an in-place restore: it overwrites live files.
func TestInPlaceRestoreRequiresExplicitConfirmation(t *testing.T) {
	err := run(context.Background(), []string{
		"backup", "restore", "-account", "a", "-backup", "b", "-snapshot", "s", "-in-place",
	})
	if err == nil {
		t.Fatal("in-place restore without --yes should be refused")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should point at the confirmation flag, got: %v", err)
	}
}

// Non-destructive actions must not demand confirmation.
func TestSuspendDoesNotRequireConfirmation(t *testing.T) {
	// Reaches the network layer and fails there (no session), which is proof
	// the confirmation guard did not block it.
	err := run(context.Background(), []string{"accounts", "suspend", "-id", "abc"})
	if err != nil && strings.Contains(err.Error(), "--yes") {
		t.Errorf("suspend should not require confirmation, got: %v", err)
	}
}

func TestMissingRequiredFlagsAreReported(t *testing.T) {
	cases := [][]string{
		{"accounts", "create"},
		{"dns", "set", "-account", "a"},
		{"dns", "delete", "-account", "a"},
		{"ssl", "issue"},
		{"backup", "run", "-account", "a"},
		{"backup", "list"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := run(context.Background(), args); err == nil {
				t.Error("expected an error for missing required flags")
			}
		})
	}
}

func TestQBuildsQueryStrings(t *testing.T) {
	if got := q("region", ""); got != "" {
		t.Errorf("empty values should be omitted entirely, got %q", got)
	}
	if got := q("region", "eu-west"); got != "?region=eu-west" {
		t.Errorf("q = %q", got)
	}
	// Values must be escaped — a record name can contain characters that would
	// otherwise break out of the query string.
	if got := q("name", "a b&c"); !strings.Contains(got, "a+b%26c") {
		t.Errorf("value was not escaped: %q", got)
	}
}
