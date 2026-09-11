package scheduler

// One GitHub App delivery → every application it should deploy
// (github-app.md §6).
//
// EVERY one, deliberately. A repository can legitimately be deployed by several
// environments — staging and production from the same `main`, or two
// applications from a monorepo — and picking one would silently skip the rest.
// The per-application webhook cannot have this problem because its URL names
// the application; the App's single endpoint has to resolve it, so resolving it
// to a set is the only correct answer.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// pushEvent is the slice of GitHub's push payload this needs.
type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// DeployFromPush deploys every application whose repository and branch the push
// matches, and reports how many it started.
func (s *Scheduler) DeployFromPush(ctx context.Context, payload []byte) (int, error) {
	var ev pushEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return 0, fmt.Errorf("scheduler: parsing the push payload: %w", err)
	}
	// A branch DELETION is a push with `deleted: true` and a null commit.
	// Deploying it would check out nothing; preview environments already reap
	// themselves on their own signal.
	if ev.Deleted || ev.Repository.FullName == "" {
		return 0, nil
	}
	branch, ok := strings.CutPrefix(ev.Ref, "refs/heads/")
	if !ok {
		// A tag or a note. Deploying on tags is a real feature and a different
		// one: it needs a rule for WHICH tags, and inventing one here would
		// deploy on every `v`-anything somebody pushed.
		return 0, nil
	}

	apps, err := s.store.ListApplicationsByRepo(ctx, ev.Repository.FullName, branch)
	if err != nil {
		return 0, fmt.Errorf("scheduler: finding applications for %s: %w", ev.Repository.FullName, err)
	}
	started := 0
	for _, app := range apps {
		// "webhook" is the trigger the per-application path already records, and
		// this IS that path with a different door: the deployment history must
		// not grow a second word for the same cause.
		if _, err := s.Deploy(ctx, app.ID, "webhook", ev.After); err != nil {
			// One application's refusal — a freeze window, a pending approval,
			// a quota — must not stop the others. It is already recorded on
			// that application's own deployment history and in the audit log.
			s.log.Warn("push deploy refused", "app_id", app.ID, "repo", ev.Repository.FullName, "error", err)
			continue
		}
		started++
	}
	return started, nil
}
