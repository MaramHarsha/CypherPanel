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
   approval, require the CI and Integration checks to pass, **tick "Dismiss
   stale pull request approvals when new commits are pushed"**, and **leave
   auto-merge disabled**.

   That tick box is load-bearing. Without it GitHub keeps an approval when new
   commits land, so a bot approval of an earlier commit still satisfies the
   required approval for code it was never given for. The bot dismisses its own
   stale approvals as a backstop, but branch protection is the guard that holds
   even if the workflow fails to run.

Until steps 1–5 are done the workflows run and fail on the missing secret; the
bot is not operational before that.

## What it does

- **Assign** (`pr-review-assign.yml`, on `pull_request_target`): requests the
  bot as reviewer on opened / reopened / ready-for-review. Skips drafts, its own
  pull requests, and anything it has already been requested on.
- **Approve** (`pr-auto-approve.yml`, on `workflow_run` after CI or
  Integration): re-reads the pull request through the API and approves only if
  every rule below holds.
- **Approval guard** (`pr-approval-guard.yml`, on `pull_request_target`
  labeled / unlabeled / synchronize): **dismisses** the bot's approval when a
  blocked label appears, or when a new commit makes it stale.
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
| No sensitive path touched, on **either side** of a rename | otherwise one comment explaining manual review is needed |
| Author on the trusted list | otherwise skip |
| Bot has not already approved **this** commit | otherwise skip |

An approval of an *older* commit never counts for the current one — the bot
will not issue one, and dismisses its own if a commit lands afterwards. Pair it
with branch protection's stale-review dismissal so this holds even when the
workflow does not run.

**Renames are judged by both paths.** GitHub reports a rename's destination as
`filename` and its source as `previous_filename`. Matching only the destination
would let a pull request move `core/auth/session.go` somewhere unprotected
*while editing it* and draw no attention — so both paths are matched, and the
protected one is what gets named in the comment.

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
- Approval and the label guard share **one** concurrency group, keyed on the
  head SHA, so a blocked label cannot race an in-flight approval. Separate
  groups let both interleave: the approval read the old label set while the
  guard found no review yet to dismiss, and the approval landed under the label
  anyway.
- The approval decision is re-run against a freshly read pull request in the
  instant before the review is posted. The shared group closes the wide window;
  this closes the remaining one between the last read and the write, and also
  catches a push that landed mid-run.
- Paginated reads fail closed on truncation: a pull request larger than the
  page cap yields no data rather than a partial list, so a protected file on a
  later page cannot be missed into an approval.

## Tests

```sh
node --test .github/review-bot/policy.test.mjs
```

26 cases, no token and no network — the policy is a pure function over
fixtures. They run in CI inside the existing "Format & lint" job.
