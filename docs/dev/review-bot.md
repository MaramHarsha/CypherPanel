# cypherpanel-review-bot — setup

A CI-and-policy approval bot. It requests itself as a reviewer on new pull
requests, and after CI passes it approves ones that clear a fixed policy. It
**never merges** — merging stays manual, and nothing in the automation can
merge, enable auto-merge, or push to a protected branch.

It does not read your code. The approval says so explicitly.

## What you must do by hand

1. **Create the GitHub account** `cypherpanel-review-bot` (a machine user; it
   needs its own email address).
2. **Enable two-factor authentication** on it.
3. **Add it as a collaborator** on this repository with **Write** access.
   Write is required to submit reviews; Read cannot approve.
4. **Sign in as the bot** and create a **fine-grained personal access token**:
   - Resource owner: your account · Repository access: **only** CypherPanel
   - Repository permissions: **Pull requests: Read and write**,
     **Contents: Read-only**, **Metadata: Read-only**, **Checks: Read-only**
   - Nothing else. No Actions, no Administration, no Workflows, no org scopes.
   - Set an expiry and calendar a rotation.
5. **Add the token** to this repository as the secret
   `CYPHERPANEL_REVIEW_BOT_TOKEN` (Settings → Secrets and variables → Actions).
6. **Branch protection** on `main`: require pull requests, require **1**
   approval, require the CI and Integration checks to pass, and **leave
   auto-merge disabled**.

Until steps 1–5 are done the workflows run and fail on the missing secret; the
bot is not operational before that.

## What it does

- **Assign** (`pr-review-assign.yml`, on `pull_request_target`): requests the
  bot as reviewer on opened / reopened / ready-for-review. Skips drafts, its own
  pull requests, and anything it has already been requested on.
- **Approve** (`pr-auto-approve.yml`, on `workflow_run` after CI or
  Integration): re-reads the pull request through the API and approves only if
  every rule below holds.
- **Label guard** (`pr-label-guard.yml`, on `pull_request_target` labeled /
  unlabeled): **dismisses** the bot's approval when a blocked label appears.
  Approval is driven by CI finishing, and labelling does not re-run CI — so
  without this a "hands off" label added *after* an approval changed nothing
  and the stale approval kept satisfying the one-approval merge gate.

  Removing the label deliberately does **not** restore the approval: that must
  go back through the CI-verified path, so push a commit or re-run CI.
  Re-approving on `unlabeled` would approve without re-checking the head.

## The rules, in order

Every one fails closed — anything unreadable, unknown, or ambiguous means *do
not approve*.

| Rule | Effect |
|---|---|
| PR open, not draft, not authored by the bot | otherwise skip |
| `mergeable == true` (a `null` "still computing" is not good enough) | otherwise skip |
| CI's tested commit **is** the current head | otherwise skip — this is what stops "CI passed, then someone pushed" |
| Every required check reported for that commit, `completed` + `success` | pending / failure / cancelled / timed_out / **skipped** all block |
| No blocked label | otherwise skip |
| No sensitive path touched | otherwise one comment explaining manual review is needed |
| Author on the trusted list | otherwise skip |
| Bot has not already approved **this** commit | otherwise skip |

An approval of an *older* commit never counts for the current one.

**Sensitive paths beat trusted authors.** A Dependabot bump touches `go.sum` or
`pnpm-lock.yaml`, which are sensitive, so those are not auto-approved — a
poisoned dependency is a common supply-chain route and the case where a human
glance is worth most.

## Configuring it

Everything tunable is in `.github/review-bot/config.json`: `trustedAuthors`,
`blockedLabels`, `requiredChecks`, `sensitivePaths`. `requiredChecks` must match
the job **names** in `ci.yml` and `integration.yml` — if you rename a job,
rename it here, or the bot will refuse to approve because the check "did not
report".

Skipped counting as a block is deliberate: no job here is path-filtered today,
so a skip is anomalous. Add path filters later and you will want to allow them.

## Security properties

- Approval runs on `workflow_run`, so the workflow definition comes from the
  default branch and a fork cannot alter what executes.
- Neither workflow checks out or executes pull-request code — both pin
  `ref: default_branch`, with `persist-credentials: false`.
- All pull-request data arrives through the API. No PR-controlled string is
  ever interpolated into a shell command.
- `permissions: {}` at workflow level; the bot token is the only credential and
  it is repository-scoped with pull-request write as its widest grant.
- Concurrency is keyed on the head SHA, so two runs cannot race into two
  approvals.

## Tests

```sh
node --test .github/review-bot/policy.test.mjs
```

26 cases, no token and no network — the policy is a pure function over
fixtures. They run in CI inside the existing "Format & lint" job.
