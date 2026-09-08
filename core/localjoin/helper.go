package localjoin

// The root one-shot (local-server.md §3).
//
// It runs THE SAME install/agent.sh the pasted command runs, with the same
// variables — not a reimplementation of it. Everything that path learned (the
// Docker check, the CA pin, the ELF sanity check, the role flag, the systemd
// unit) applies unchanged, and a fix to agent.sh fixes both paths at once. The
// alternative — a second installer in Go — would be a second thing to keep
// correct and the one that gets it wrong is always the one nobody runs by hand.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// installTimeout bounds the whole run. The installer pulls a binary and starts
// a unit; ten minutes is generous for both and finite, which a one-shot that
// hangs forever holding a path unit is not.
const installTimeout = 10 * time.Minute

// Runner executes the installer. Consumer-defined so the test can assert what
// the helper would have run without installing anything on the test machine.
type Runner interface {
	Run(ctx context.Context, script string, env []string) ([]byte, error)
}

// ShellRunner pipes the script to `sh` exactly as `curl … | sh` does.
type ShellRunner struct{}

func (ShellRunner) Run(ctx context.Context, script string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	// A MINIMAL environment. The helper runs from a unit whose EnvironmentFile
	// is cypherd.env — the master key, the database password, the setup code —
	// and the first version handed all of that to the installer and to every
	// process it spawns (systemctl, curl, the agent it starts). The installer
	// needs a PATH and the join variables, and nothing else.
	cmd.Env = append(minimalEnv(), env...)
	return cmd.CombinedOutput()
}

// minimalEnv carries over only what a shell script needs to find its tools.
func minimalEnv() []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	if len(out) == 0 || !strings.HasPrefix(out[0], "PATH=") {
		out = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, out...)
	}
	return out
}

// Options wires the helper.
type Options struct {
	Dir Dir
	// Script is the installer's text. It comes from the binary's own embedded
	// copy (the same bytes served at /install/agent.sh), so the helper does not
	// fetch over the network to install on the machine it is already on.
	Script string
	Runner Runner
	Log    *slog.Logger
	Now    func() time.Time
}

// Run consumes one request and performs it. It is the whole helper: read (and
// delete) the request, run the installer, write the outcome.
func Run(ctx context.Context, o Options) error {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Runner == nil {
		o.Runner = ShellRunner{}
	}
	if o.Dir == "" {
		return fmt.Errorf("localjoin: no handoff directory is configured")
	}

	req, err := o.Dir.ReadRequest()
	if errors.Is(err, os.ErrNotExist) {
		// The path unit fires on the file appearing; a race with a previous run
		// that already consumed it is ordinary, not an error.
		o.Log.Info("localjoin: no request to act on")
		return nil
	}
	if err != nil {
		return err
	}
	if req.Expired(o.Now()) {
		// The token in it is short-lived too, so this is belt and braces — but
		// the failure it prevents (an enrollment nobody is watching, minutes
		// after the click) is exactly the one a path unit makes possible.
		o.Log.Warn("localjoin: refusing an expired request", "id", req.ID, "expired_at", req.ExpiresAt)
		return o.Dir.WriteStatus(Status{
			RequestID: req.ID, ServerID: req.ServerID, Phase: PhaseFailed,
			Detail: "the request expired before the helper ran; try again from the panel",
		})
	}
	if req.Token == "" || req.EnrollAddr == "" || req.PlaneHTTP == "" || req.CAFingerprint == "" {
		return o.Dir.WriteStatus(Status{
			RequestID: req.ID, ServerID: req.ServerID, Phase: PhaseFailed,
			Detail: "the request was incomplete",
		})
	}

	if err := o.Dir.WriteStatus(Status{
		RequestID: req.ID, ServerID: req.ServerID, Phase: PhaseInstalling,
		Detail: "installing the agent on this host",
	}); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()

	o.Log.Info("localjoin: installing the agent on this host", "server_id", req.ServerID)
	out, runErr := o.Runner.Run(ctx, o.Script, envFor(req))
	if runErr != nil {
		// The installer's own last line is the useful part; the whole log is in
		// the journal. NEVER the environment — it carries the join token.
		o.Log.Error("localjoin: the installer failed", "server_id", req.ServerID, "error", runErr)
		return o.Dir.WriteStatus(Status{
			RequestID: req.ID, ServerID: req.ServerID, Phase: PhaseFailed,
			Detail: lastLine(string(out), runErr),
		})
	}

	o.Log.Info("localjoin: the agent is installed and enrolling", "server_id", req.ServerID)
	return o.Dir.WriteStatus(Status{
		RequestID: req.ID, ServerID: req.ServerID, Phase: PhaseSucceeded,
		Detail: "the agent is installed and enrolled; it appears in the fleet within one heartbeat",
	})
}

// envFor is the pasted command's own variable list. Keeping it identical is the
// point: the two paths differ in who types them and in nothing else.
func envFor(r Request) []string {
	env := []string{
		"CYPHER_PLANE=" + r.EnrollAddr,
		"CYPHER_PLANE_HTTP=" + r.PlaneHTTP,
		"CYPHER_TOKEN=" + r.Token,
		"CYPHER_CA_FINGERPRINT=" + r.CAFingerprint,
	}
	if r.AgentURL != "" {
		env = append(env, "CYPHER_AGENT_URL="+r.AgentURL)
	}
	return env
}

// lastLine pulls the installer's reason out for the dialog; the exit status
// alone is not one.
//
// The installer's `fail` prints "error: <reason>" and exits, and the reason is
// often SEVERAL lines — "could not download the agent binary from …" followed
// by the remedies. The first version of this took the last non-empty line, and
// on a fresh host with no release published the dialog therefore read
// "Building from a source checkout: cd agent && go build …" — the tail of the
// remedy, with the failure itself scrolled off. The line that starts with
// "error:" is the sentence; everything after it is advice.
func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(stripANSI(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "error:") {
			return line
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return err.Error()
}

// stripANSI removes the installer's colour codes, which are for a terminal and
// become noise in a dialog.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		for i < len(s) && s[i] != 'm' {
			i++
		}
	}
	return b.String()
}
