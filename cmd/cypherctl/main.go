// cypherctl is the CypherPanel command-line client.
//
// It is a thin layer over the same REST API the web UI uses (plan.md §14):
// every command maps to an endpoint, and no business logic lives here. If a
// command seems to need logic of its own, that logic belongs in CypherCore
// behind an endpoint instead — otherwise the CLI and the panel drift apart.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/MaramHarsha/CypherPanel/internal/version"
)

type commandFunc func(ctx context.Context, args []string) error

type command struct {
	name    string
	summary string
	run     commandFunc
}

// newFlagSet builds a flag set that reports usage to stderr under the full
// command path, so `cypherctl accounts create --help` reads correctly.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: cypherctl %s [flags]\n\nFlags:\n", name)
		fs.PrintDefaults()
	}
	return fs
}

// group is a namespace of subcommands (e.g. `accounts`).
type group struct {
	summary string
	subs    map[string]command
}

var groups = map[string]group{
	"servers": {
		summary: "Inspect the server fleet",
		subs: map[string]command{
			"list": {"list", "List registered servers", cmdServersList},
		},
	},
	"accounts": {
		summary: "Manage hosting accounts",
		subs: map[string]command{
			"list":      {"list", "List hosting accounts", cmdAccountsList},
			"create":    {"create", "Provision a new hosting account", cmdAccountsCreate},
			"suspend":   {"suspend", "Suspend an account", cmdAccountsAction("suspend")},
			"unsuspend": {"unsuspend", "Restore a suspended account", cmdAccountsAction("unsuspend")},
			"terminate": {"terminate", "Permanently delete an account", cmdAccountsAction("terminate")},
		},
	},
	"dns": {
		summary: "Manage DNS records",
		subs: map[string]command{
			"list":   {"list", "List an account's DNS records", cmdDNSList},
			"set":    {"set", "Create or update a DNS record", cmdDNSSet},
			"delete": {"delete", "Delete a DNS record", cmdDNSDelete},
		},
	},
	"ssl": {
		summary: "Manage TLS certificates",
		subs: map[string]command{
			"issue": {"issue", "Request a Let's Encrypt certificate", cmdSSLIssue},
		},
	},
	"backup": {
		summary: "Run and restore backups",
		subs: map[string]command{
			"destinations": {"destinations", "List backup destinations", cmdBackupDestinations},
			"run":          {"run", "Back up an account now", cmdBackupRun},
			"list":         {"list", "List an account's backups", cmdBackupList},
			"restore":      {"restore", "Restore an account from a snapshot", cmdBackupRestore},
		},
	},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage()
		return nil
	}

	switch args[0] {
	case "version":
		fmt.Printf("cypherctl %s\n", version.Core)
		return nil
	case "login":
		return cmdLogin(ctx, args[1:])
	case "logout":
		return cmdLogout(ctx, args[1:])
	}

	g, ok := groups[args[0]]
	if !ok {
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
	if len(args) == 1 {
		groupUsage(args[0], g)
		return fmt.Errorf("%s needs a subcommand", args[0])
	}
	sub, ok := g.subs[args[1]]
	if !ok {
		groupUsage(args[0], g)
		return fmt.Errorf("unknown %s subcommand %q", args[0], args[1])
	}
	return sub.run(ctx, args[2:])
}

func cmdLogin(ctx context.Context, args []string) error {
	fs := newFlagSet("login")
	apiURL := fs.String("url", envOr("CYPHER_API_URL", "http://localhost:8080"), "CypherCore base URL")
	username := fs.String("username", "", "panel username (required)")
	// A password on the command line lands in the shell history and the process
	// table, so the environment variable is the documented path for scripts.
	password := fs.String("password", os.Getenv("CYPHERCTL_PASSWORD"), "panel password (prefer CYPHERCTL_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" {
		fs.Usage()
		return errors.New("--username and a password (--password or CYPHERCTL_PASSWORD) are required")
	}
	if err := login(ctx, *apiURL, *username, *password); err != nil {
		return err
	}
	fmt.Printf("logged in to %s as %s\n", *apiURL, *username)
	return nil
}

func cmdLogout(_ context.Context, args []string) error {
	fs := newFlagSet("logout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := clearCredentials(); err != nil {
		return err
	}
	fmt.Println("logged out")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func usage() {
	var b strings.Builder
	b.WriteString("cypherctl — CypherPanel command-line client\n\n")
	b.WriteString("Usage:\n  cypherctl <command> [subcommand] [flags]\n\n")
	b.WriteString("Session:\n")
	b.WriteString("  login                 Authenticate and store a session\n")
	b.WriteString("  logout                Discard the stored session\n")
	b.WriteString("  version               Print the client version\n\n")
	b.WriteString("Commands:\n")

	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "  %-20s  %s\n", name, groups[name].summary)
	}
	b.WriteString("\nRun `cypherctl <command>` to list its subcommands.\n")
	b.WriteString("\nThe API base URL comes from --url at login (or CYPHER_API_URL).\n")
	fmt.Fprint(os.Stderr, b.String())
}

func groupUsage(name string, g group) {
	fmt.Fprintf(os.Stderr, "cypherctl %s — %s\n\nSubcommands:\n", name, g.summary)
	subs := make([]string, 0, len(g.subs))
	for s := range g.subs {
		subs = append(subs, s)
	}
	sort.Strings(subs)
	for _, s := range subs {
		fmt.Fprintf(os.Stderr, "  %-20s  %s\n", s, g.subs[s].summary)
	}
	fmt.Fprintf(os.Stderr, "\nRun `cypherctl %s <subcommand> --help` for flags.\n", name)
}
