//go:build linux

package cron

import (
	"bytes"
	"context"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
	"time"
)

// usernameRe guards the username before it becomes a `crontab -u` argument.
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Handle runs a crontab get/set for the account user via `crontab -u`, which
// installs the crontab under that user's identity (jobs execute as them).
func Handle(req Request) Response {
	if !usernameRe.MatchString(req.Username) {
		return Response{Error: "invalid account user"}
	}
	if _, err := user.Lookup(req.Username); err != nil {
		return Response{Error: "unknown account user"}
	}
	if _, err := exec.LookPath("crontab"); err != nil {
		return Response{Error: "cron is not available on this server"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch req.Op {
	case OpGet:
		out, err := exec.CommandContext(ctx, "crontab", "-u", req.Username, "-l").CombinedOutput()
		if err != nil {
			// "no crontab for X" is not an error for the editor — return empty.
			if strings.Contains(string(out), "no crontab") {
				return Response{Content: ""}
			}
			return Response{Error: strings.TrimSpace(string(out))}
		}
		return Response{Content: string(out)}

	case OpSet:
		cmd := exec.CommandContext(ctx, "crontab", "-u", req.Username, "-")
		cmd.Stdin = strings.NewReader(ensureTrailingNewline(req.Content))
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			// crontab validates on install; surface its message.
			return Response{Error: strings.TrimSpace(errb.String())}
		}
		return Response{Content: req.Content}

	default:
		return Response{Error: "unknown cron op"}
	}
}

func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
