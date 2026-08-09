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
4. **Sign in as the bot** and create a **classic personal access token** with
   the single **`repo`** scope. Set an expiry and calendar a rotation.

   Not a fine-grained token, and this is not a preference. A fine-grained token
   is issued against one *resource owner*, and the bot can only choose itself or
   an organisation it belongs to — being a collaborator on a repository owned by
   another personal account does not put that repository on the list. A
   fine-grained token created by the bot therefore cannot select
   `MaramHarsha/CypherPanel` at all, and every workflow would fail on a 404 that
   reads like a missing secret rather than an unusable one.

   The cost is honest: classic `repo` is broader than we want, and it covers
   every repository the bot account can reach. That is survivable *because* the
   bot account exists only for this, holds no other collaborations, and has 2FA.
   Two ways to get a properly narrow token, when either becomes worth the setup:

   - **A GitHub App** installed on this repository, with Pull requests: write
     and Contents/Metadata/Checks: read. Per-repository permissions, no user
     account, tokens that expire hourly. The right end state; it needs the
     workflows to mint an installation token instead of reading a secret.
   - **Move the repository under an organisation** the bot can be a member of,
     which makes the organisation selectable as a fine-grained resource owner.
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
- **Command** (`pr-command.yml`, on `issue_comment`): acts on
  `@cypherpanel-review-bot approve` from the repository owner. See below.

## The standing comment

When a pull request touches protected paths the bot posts **one** comment and
then keeps it current: on every later commit it is **edited in place** to name
the commit it was computed from and the paths that commit actually touches.

Both halves matter. Posting again per commit would leave a wall of near
identical comments on any branch that takes more than an afternoon, which is
how a comment stops being read. Posting once and then falling silent — what it
used to do — left a verdict that looked live but described a commit from days
earlier, listing files the pull request might no longer touch; the log said
`sensitive-change comment already present` and nothing else ever appeared.

The body is the whole state, so the write is idempotent by content: CI and
Integration both finishing produce two runs that compute the same body, and the
second one changes nothing. When the protected paths go away, or the owner
reviews them, the same comment is rewritten to say so rather than deleted — the
marker in it is what lets a later commit turn it back into a verdict.

## Approving protected changes: `@cypherpanel-review-bot approve`

The bot refuses to approve protected changes by itself, and that refusal needed
an answer. Without one the only ways past it were to merge around your own
required approval or to approve as yourself — neither of which leaves the trail
the bot's approval does, and the first of which erodes the branch protection
that makes the rest of this worth having.

Comment `@cypherpanel-review-bot approve` on the pull request:

- **Only the repository owner.** Two independent facts must agree: the login is
  on `commandApprovers` in the config, *and* GitHub's own `author_association`
  for the comment is `OWNER`. The second is computed by GitHub from the
  account's real relationship to this repository, so a commenter cannot set it —
  which is what stops a recycled or lookalike login from inheriting the
  authority the list grants to a name. This repository is public: everyone else
  who types the command is **ignored in silence**, deliberately, because
  replying would turn the command into a way to make the bot post on demand.
- **It substitutes for the protected-path check and for nothing else.** Every
  required check must still be green **on the current head**, the tested commit
  must still be the current one, and a blocked label still wins. The approval
  body says so, and names who asked for it.
- **It is for one commit.** The authorization is recorded against the head sha
  at the time of the command; push anything and it expires (and the guard
  dismisses the approval it produced). That is the point — an authorization is
  given for the diff its author read.
- **It works before CI is green.** If the checks are still running the bot
  records the authorization in a comment of its own and approves as soon as
  they pass. The record is a comment *the bot* wrote, so nobody who cannot post
  as the bot can forge one; deleting that comment withdraws it, and a blocked
  label overrides it.
- The command is ignored inside code spans, fenced blocks, HTML comments and
  quoted lines — otherwise pasting this paragraph into a review thread would
  approve something.

## The rules, in order

Every one fails closed — anything unreadable, unknown, or ambiguous means *do
not approve*.

| Rule | Effect |
|---|---|
| PR open, not draft, not authored by the bot | otherwise skip |
| `mergeable == true` (a `null` "still computing" is not good enough) | otherwise skip |
| CI's tested commit **is** the current head | otherwise skip — this is what stops "CI passed, then someone pushed" |
| Every required check reported for that commit, `completed` + `success` | pending / failure / cancelled / timed_out / **skipped** all block |
| No blocked label | otherwise skip — an owner authorization does **not** override this |
| No sensitive path touched, on **either side** of a rename | otherwise the standing comment explains why manual review is needed — unless the owner has authorized this commit |
| Author on the trusted list | otherwise skip — unless the owner has authorized this commit |
| Bot has not already approved **this** commit | otherwise skip |

An approval of an *older* commit never counts for the current one — the bot
will not issue one, and dismisses its own if a commit lands afterwards. Pair it
with branch protection's stale-review dismissal so this holds even when the
workflow does not run.

**Authorization is enforced at the call sites, not just where it is defined.**
Thirteen handler files call `require*Role` / `authorizeResolved`, so removing one
such line takes an endpoint's role gate off with nothing else in the diff — which
is why `core/api/rest/*.go` is protected wholesale rather than a short list of
"boundary" files. `core/domain/team.go` defines `RoleRank`, the ordering every
minimum-rank check compares against: reorder it once and every call site weakens
at the same time. `core/teams/`, `core/onboarding/` and the team store round it
out. The pattern is `*.go`, not `**`, so the committed web UI build output under
`core/api/rest/webui/dist/` stays approvable — every frontend pull request
rewrites it, and a comment that fires on all of them is one nobody reads.

**The authorization boundary is wider than `core/auth/**`.** `core/api/rest/rest.go`
holds the route table, where each route declares whether it is `a.authed(...)`
— deleting that one call un-authenticates an endpoint. `core/api/rest/authz.go`
holds every role check and the resource-to-project resolution they depend on.
Token issuance, TOTP, and team/user role granting sit in their own handlers, and
`core/api/grpc/` exchanges a join token for an agent certificate and derives
caller identity from the mTLS CommonName. All of those are protected; the
remaining handlers are not, deliberately — a comment that fires on every backend
pull request is a comment that gets ignored, which is how a real one gets waved
through.

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
`blockedLabels`, `requiredChecks`, `sensitivePaths`, and — for the command —
`commandApprovers` (logins) and `commandAssociations` (the `author_association`
values GitHub must independently agree with; `["OWNER"]` here). Widening
`commandApprovers` alone changes nothing unless the added account genuinely
holds one of those associations, which is the intent: the config states policy,
GitHub states fact, and both have to say yes. `requiredChecks` must match
the job **names** in `ci.yml` and `integration.yml` — if you rename a job,
rename it here, or the bot will refuse to approve because the check "did not
report".

Skipped counting as a block is deliberate: no job here is path-filtered today,
so a skip is anomalous. Add path filters later and you will want to allow them.

## Security properties

- Approval runs on `workflow_run`, so the workflow definition comes from the
  default branch and a fork cannot alter what executes.
- No workflow checks out or executes pull-request code — all four pin
  `ref: default_branch`, with `persist-credentials: false`.
- All pull-request data arrives through the API. No PR-controlled string is
  ever interpolated into a shell command.
- The command workflow never handles the comment body: it passes the script a
  comment **id**, and the script reads the body back from the API. A comment is
  the one input a stranger writes directly, so it never touches the workflow
  file, where `${{ }}` is substituted into the shell.
- The command's `if:` is a pre-filter, not the authorization — it keeps a public
  repository's comment traffic from starting runners. Authorization is decided
  in `policy.mjs`, against the comment as the API reports it, and tested there.
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

56 cases, no token and no network — the policy is a pure function over
fixtures. They run in CI inside the existing "Format & lint" job.
