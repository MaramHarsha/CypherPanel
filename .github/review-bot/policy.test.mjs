// node --test .github/review-bot
//
// Every case is a call into the pure decision function with fixtures, so the
// suite needs no token, no network, and no GitHub.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  decide, approvalBody, sensitiveBody, alreadyCommented, shouldRequestReview, matchesGlob,
  decideOnLabels,
  pathsOf, dismissalMessage, blockedLabelsOf,
} from "./policy.mjs";

const config = JSON.parse(readFileSync(new URL("./config.json", import.meta.url)));
const SHA = "abcdef1234567890";
const OLD = "0000000deadbeef0";

const green = (sha = SHA) =>
  config.requiredChecks.map((name) => ({ name, head_sha: sha, status: "completed", conclusion: "success" }));

const prOf = (o = {}) => ({
  state: "open", draft: false, mergeable: true, labels: [],
  user: { login: "MaramHarsha" }, head: { sha: SHA }, ...o,
});

const call = (o = {}) => decide({
  pr: prOf(o.pr), checks: o.checks ?? green(), reviews: o.reviews ?? [],
  files: o.files ?? ["README.md"], triggerSha: o.triggerSha ?? SHA, config,
});

test("approves: green checks, trusted author, nothing sensitive", () => {
  assert.equal(call().action, "approve");
});

test("blocks: a required check failed", () => {
  const checks = green();
  checks[2] = { ...checks[2], conclusion: "failure" };
  const d = call({ checks });
  assert.equal(d.action, "skip");
  assert.match(d.reason, /concluded failure/);
});

test("blocks: a required check is still pending", () => {
  const checks = green();
  checks[0] = { ...checks[0], status: "in_progress", conclusion: null };
  assert.match(call({ checks }).reason, /in_progress/);
});

test("blocks: a required check was skipped or cancelled", () => {
  for (const conclusion of ["skipped", "cancelled", "timed_out", "neutral"]) {
    const checks = green();
    checks[1] = { ...checks[1], conclusion };
    assert.equal(call({ checks }).action, "skip", conclusion);
  }
});

test("blocks: a required check never reported at all", () => {
  const checks = green().slice(1);
  assert.match(call({ checks }).reason, /did not report/);
});

test("blocks: draft pull request", () => {
  assert.match(call({ pr: { draft: true } }).reason, /draft/);
});

test("blocks: pull request is closed", () => {
  assert.match(call({ pr: { state: "closed" } }).reason, /closed/);
});

test("blocks: a new commit landed after CI ran", () => {
  // CI was green — but for the previous head.
  const d = call({ triggerSha: OLD, checks: green(OLD) });
  assert.equal(d.action, "skip");
  assert.match(d.reason, /newer commit was pushed/);
});

test("blocks: checks are green but belong to a different commit", () => {
  // The tested sha matches head, yet the runs are stamped for another commit.
  assert.match(call({ checks: green(OLD) }).reason, /did not report/);
});

test("blocks: merge conflicts, and also unknown mergeability", () => {
  assert.match(call({ pr: { mergeable: false } }).reason, /conflict/);
  assert.match(call({ pr: { mergeable: null } }).reason, /still unknown/);
});

test("idempotent: already approved at this commit", () => {
  const reviews = [{ user: { login: config.botLogin }, state: "APPROVED", commit_id: SHA }];
  assert.match(call({ reviews }).reason, /already approved/);
});

test("an approval of an OLDER commit does not count for the current one", () => {
  const reviews = [{ user: { login: config.botLogin }, state: "APPROVED", commit_id: OLD }];
  assert.equal(call({ reviews }).action, "approve");
});

test("a human approval does not satisfy the bot's idempotency check", () => {
  const reviews = [{ user: { login: "SomeoneElse" }, state: "APPROVED", commit_id: SHA }];
  assert.equal(call({ reviews }).action, "approve");
});

test("blocks: every blocked label", () => {
  for (const name of config.blockedLabels) {
    const d = call({ pr: { labels: [{ name }] } });
    assert.equal(d.action, "skip", name);
    assert.match(d.reason, /blocked label/);
  }
});

test("sensitive changes ask for manual review instead of approving", () => {
  const d = call({ files: [".github/workflows/ci.yml"] });
  assert.equal(d.action, "comment-sensitive");
  assert.deepEqual(d.paths, [".github/workflows/ci.yml"]);
});

test("sensitive surface covers auth, crypto, migrations, contracts, deploy, deps", () => {
  const cases = [
    "core/auth/session.go", "pkg/pki/ca.go", "core/secret/box.go",
    "core/store/migrations/0012_x.sql", "proto/cypherpanel/agent/v1/work.proto",
    "core/api/rest/openapi.yaml", "go.sum", "web/pnpm-lock.yaml",
    "Dockerfile", "install/install.sh", "deploy/docker-compose.yml", "CODEOWNERS",
  ];
  for (const f of cases) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
});

test("sensitive beats trusted author — dependabot lockfiles are not auto-approved", () => {
  const d = call({ pr: { user: { login: "dependabot[bot]" } }, files: ["go.sum"] });
  assert.equal(d.action, "comment-sensitive");
});

test("trusted bots are approved for ordinary changes", () => {
  for (const login of config.trustedAuthors) {
    assert.equal(call({ pr: { user: { login } } }).action, "approve", login);
  }
});

test("blocks: external contributor", () => {
  const d = call({ pr: { user: { login: "outside-contributor" } } });
  assert.equal(d.action, "skip");
  assert.match(d.reason, /not on the trusted list/);
});

test("blocks: pull request authored by the bot itself", () => {
  assert.match(call({ pr: { user: { login: config.botLogin } } }).reason, /opened by the bot/);
});

test("fails closed when any API response is missing or malformed", () => {
  const base = { pr: prOf(), checks: green(), reviews: [], files: [], triggerSha: SHA, config };
  for (const key of ["pr", "checks", "reviews", "files", "triggerSha", "config"]) {
    assert.equal(decide({ ...base, [key]: undefined }).action, "skip", key);
    assert.equal(decide({ ...base, [key]: null }).action, "skip", `${key} null`);
  }
  assert.equal(decide(undefined).action, "skip");
  assert.equal(decide({ ...base, checks: { not: "an array" } }).action, "skip");
  assert.equal(decide({ ...base, pr: { ...prOf(), head: {} } }).action, "skip");
  assert.equal(decide({ ...base, pr: { ...prOf(), user: {} } }).action, "skip");
});

test("re-running on an already-approved commit is a no-op, not a second approval", () => {
  const reviews = [{ user: { login: config.botLogin }, state: "APPROVED", commit_id: SHA }];
  assert.equal(call({ reviews }).action, "skip");
  assert.equal(call({ reviews }).action, "skip");
});

test("the sensitive comment is posted once", () => {
  const marker = config.commentMarker;
  const body = sensitiveBody([".github/workflows/ci.yml"], marker);
  assert.ok(body.includes(marker));
  assert.equal(alreadyCommented([], config.botLogin, marker), false);
  assert.equal(alreadyCommented([{ user: { login: config.botLogin }, body }], config.botLogin, marker), true);
  // Unreadable comments must not cause a duplicate post.
  assert.equal(alreadyCommented(undefined, config.botLogin, marker), true);
});

test("reviewer assignment skips drafts, self, duplicates, and unreadable state", () => {
  const bot = config.botLogin;
  assert.equal(shouldRequestReview(prOf(), [], bot), true);
  assert.equal(shouldRequestReview(prOf({ draft: true }), [], bot), false);
  assert.equal(shouldRequestReview(prOf({ state: "closed" }), [], bot), false);
  assert.equal(shouldRequestReview(prOf({ user: { login: bot } }), [], bot), false);
  assert.equal(shouldRequestReview(prOf(), [{ login: bot }], bot), false);
  assert.equal(shouldRequestReview(prOf(), undefined, bot), false);
});

test("the approval body never claims a semantic review, and never claims a merge", () => {
  const body = approvalBody(SHA);
  assert.match(body, /policy/i);
  assert.match(body, /does not merge/i);
  assert.match(body, /abcdef1/);
  assert.doesNotMatch(body, /merged|merging the/i);
});

test("glob matching distinguishes * from **", () => {
  assert.ok(matchesGlob(".github/**", ".github/workflows/ci.yml"));
  assert.ok(matchesGlob("**/go.sum", "core/go.sum"));
  assert.ok(matchesGlob("go.sum", "go.sum"));
  assert.equal(matchesGlob("*.md", "docs/readme.md"), false);
  assert.equal(matchesGlob("core/auth/**", "core/authz.go"), false);
});


// A blocked label added AFTER the bot approved must take the approval back.
// Adding a label does not re-run CI, so nothing else re-evaluates the PR and
// the stale approval would keep satisfying the one-approval merge gate.
test("dismisses the bot's approval when a blocked label is added", () => {
  const reviews = [{ id: 11, user: { login: config.botLogin }, state: "APPROVED", commit_id: SHA }];
  const d = decideOnLabels(prOf({ labels: [{ name: "do-not-approve" }] }), reviews, config);
  assert.equal(d.action, "dismiss");
  assert.deepEqual(d.reviewIds, [11]);
  assert.deepEqual(d.labels, ["do-not-approve"]);
  assert.match(dismissalMessage(d), /do-not-approve/);
});

test("every configured blocked label triggers dismissal", () => {
  const reviews = [{ id: 1, user: { login: config.botLogin }, state: "APPROVED", commit_id: SHA }];
  for (const name of config.blockedLabels) {
    assert.equal(decideOnLabels(prOf({ labels: [{ name }] }), reviews, config).action, "dismiss", name);
  }
});

test("no dismissal when there is nothing to dismiss or nothing blocking", () => {
  const approved = [{ id: 1, user: { login: config.botLogin }, state: "APPROVED", commit_id: SHA }];
  // Label present, but the bot never approved.
  assert.equal(decideOnLabels(prOf({ labels: [{ name: "blocked" }] }), [], config).action, "none");
  // Approved, but no blocking label.
  assert.equal(decideOnLabels(prOf({ labels: [{ name: "enhancement" }] }), approved, config).action, "none");
  // Someone else's approval is not the bot's to dismiss.
  const other = [{ id: 2, user: { login: "MaramHarsha" }, state: "APPROVED", commit_id: SHA }];
  assert.equal(decideOnLabels(prOf({ labels: [{ name: "blocked" }] }), other, config).action, "none");
});

test("dismissal is idempotent: an already-dismissed review is not re-dismissed", () => {
  const reviews = [{ id: 1, user: { login: config.botLogin }, state: "DISMISSED", commit_id: SHA }];
  assert.equal(decideOnLabels(prOf({ labels: [{ name: "blocked" }] }), reviews, config).action, "none");
});

test("label handling fails closed on unreadable input", () => {
  assert.equal(decideOnLabels(undefined, [], config).action, "none");
  assert.equal(decideOnLabels(prOf(), undefined, config).action, "none");
  assert.equal(decideOnLabels(prOf(), [], undefined).action, "none");
  assert.deepEqual(blockedLabelsOf(undefined, config), []);
});


// GitHub keeps an approval when new commits land unless branch protection is
// configured to dismiss stale reviews. Refusing to create a NEW approval was
// never enough: the old one kept satisfying the required approval for code it
// was never given for.
test("dismisses a bot approval left behind by a new commit", () => {
  const reviews = [{ id: 7, user: { login: config.botLogin }, state: "APPROVED", commit_id: OLD }];
  const d = decideOnLabels(prOf(), reviews, config); // head is SHA, approval is OLD
  assert.equal(d.action, "dismiss");
  assert.deepEqual(d.reviewIds, [7]);
  assert.equal(d.why, "stale-commit");
  assert.match(dismissalMessage(d), /new commit was pushed/);
});

test("an approval for the current commit is left alone", () => {
  const reviews = [{ id: 7, user: { login: config.botLogin }, state: "APPROVED", commit_id: SHA }];
  assert.equal(decideOnLabels(prOf(), reviews, config).action, "none");
});

test("a blocked label dismisses even when the approval is current", () => {
  const reviews = [{ id: 7, user: { login: config.botLogin }, state: "APPROVED", commit_id: SHA }];
  const d = decideOnLabels(prOf({ labels: [{ name: "blocked" }] }), reviews, config);
  assert.equal(d.why, "blocked-label");
});

// A root dependency manifest is the file most worth protecting; the leading
// `**/` used to require a slash, so only nested copies were sensitive.
test("root-level dependency manifests are sensitive, not just nested ones", () => {
  for (const f of ["package.json", "pnpm-lock.yaml", "go.mod", "go.sum", "Dockerfile", "CODEOWNERS"]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
  for (const f of ["web/package.json", "web/pnpm-lock.yaml", "core/go.sum"]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
  // A leading **/ must not turn into "match anything".
  assert.equal(matchesGlob("**/package.json", "packagexjson"), false);
  assert.equal(matchesGlob("**/go.sum", "notgo.sum"), false);
});

// A rename reports the destination as `filename` and the source as
// `previous_filename`. Reading only the destination let a pull request move a
// protected file out of its protected path *while editing it* and draw no
// attention at all — the one change that most deserves a human.
test("a rename away from a protected path is still sensitive", () => {
  const d = call({
    files: [{ filename: "docs/notes.go", previous_filename: "core/auth/session.go" }],
  });
  assert.equal(d.action, "comment-sensitive");
  assert.deepEqual(d.paths, ["core/auth/session.go"]);
});

test("a rename INTO a protected path is sensitive too", () => {
  const d = call({ files: [{ filename: "core/auth/session.go", previous_filename: "docs/notes.go" }] });
  assert.equal(d.action, "comment-sensitive");
  assert.deepEqual(d.paths, ["core/auth/session.go"]);
});

test("object and string file entries behave identically", () => {
  assert.equal(call({ files: [{ filename: "web/src/app.tsx" }] }).action, "approve");
  assert.equal(call({ files: ["web/src/app.tsx"] }).action, "approve");
  assert.deepEqual(pathsOf({ filename: "a", previous_filename: "b" }), ["a", "b"]);
  assert.deepEqual(pathsOf({ filename: "a" }), ["a"]);
  assert.deepEqual(pathsOf("a"), ["a"]);
  assert.deepEqual(pathsOf(null), []);
});

// go.work selects which modules every build actually compiles, so editing it
// can redirect a dependency without touching any go.mod in the diff.
test("the Go workspace manifest is protected", () => {
  for (const f of ["go.work", "go.work.sum"]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
});

// core/auth/** is not the whole authorization boundary. rest.go carries the
// route table where each route declares whether it is a.authed(...) — deleting
// that one call un-authenticates an endpoint — and authz.go carries every role
// check plus the resource-to-project resolution those checks depend on. Neither
// lives under core/auth/**, so both were auto-approvable for a trusted author.
test("the REST and gRPC authorization boundary is protected", () => {
  for (const f of [
    "core/api/rest/rest.go",
    "core/api/rest/authz.go",
    "core/api/rest/handlers_auth.go",
    "core/api/rest/handlers_tokens.go",
    "core/api/rest/handlers_totp.go",
    "core/api/rest/handlers_teams.go",
    "core/api/grpc/enrollment.go",
    "core/api/grpc/relay.go",
  ]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
});

// The tests are covered alongside the code they guard: a pull request that only
// deletes authorization coverage disarms the check that would catch the next one.
test("authorization tests are protected with their code", () => {
  for (const f of ["core/api/rest/authz_test.go", "core/api/rest/rest_test.go"]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
});

// Enforcement lives at the CALL SITES, not only in authz.go. Thirteen of the
// handler files invoke require*Role/authorizeResolved, so deleting one such
// line removes an endpoint's role gate with no protected file in the diff —
// which is why the narrow "boundary files only" list was not enough and the
// whole package is protected instead.
test("every authorization enforcement point is protected", () => {
  for (const f of [
    "core/api/rest/handlers_projects.go",
    "core/api/rest/handlers_servers.go",
    "core/api/rest/handlers_applications.go",
    "core/api/rest/handlers_databases.go",
    "core/api/rest/handlers_backups.go",
    "core/api/rest/handlers_previews.go",
    "core/api/rest/handlers_deployments.go",
    "core/api/rest/handlers_scheduled_tasks.go",
    "core/api/rest/handlers_notifiers.go",
    "core/api/rest/handlers_deploy_keys.go",
    "core/api/rest/handlers_domaincheck.go",
    "core/api/rest/sse.go",
  ]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
});

// RoleRank is the shared ordering every minimum-rank check compares against:
// reorder it once and every require*Role call site weakens at the same time,
// without any of them appearing in the diff.
test("the role ordering and team membership layer are protected", () => {
  for (const f of [
    "core/domain/team.go",
    "core/teams/teams.go",
    "core/teams/teams_test.go",
    "core/store/teams.go",
    "core/store/db/teams.sql.go",
    "core/onboarding/onboarding.go",
  ]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
});

// The list still has to stop somewhere, or the comment fires on everything and
// stops being read. Code with no authorization role keeps auto-approving.
test("code outside the authorization surface still auto-approves", () => {
  for (const f of [
    "core/scheduler/scheduler.go",
    "agent/driver/docker/docker.go",
    "web/src/routes/index.tsx",
    "docs/roadmap.md",
    // The web UI is committed built, so every frontend pull request rewrites
    // these. Sweeping the package with ** would have flagged all of them as
    // touching authorization, which is exactly the noise that gets the comment
    // ignored — core/api/rest/*.go covers the Go files without the build output.
    "core/api/rest/webui/dist/assets/_app-DVgVhcSj.js",
    "core/api/rest/webui/dist/index.html",
  ]) {
    assert.equal(call({ files: [f] }).action, "approve", f);
  }
});

// The glob compiler once used a NUL byte as its `**` sentinel, which made Git
// treat this module as binary and hid every later change to the approval logic
// from review. Plain text now, and the semantics that sentinel protected still
// hold.
test("the policy source is plain text and ** still spans separators", () => {
  const src = readFileSync(new URL("./policy.mjs", import.meta.url), "utf8");
  assert.equal(src.includes("\u0000"), false, "policy.mjs must contain no NUL byte");
  assert.equal(matchesGlob("core/**/x.go", "core/a/b/x.go"), true);
  assert.equal(matchesGlob("core/*/x.go", "core/a/b/x.go"), false, "* must not span /");
  assert.equal(matchesGlob("**/go.mod", "go.mod"), true);
  assert.equal(matchesGlob("**/go.mod", "core/go.mod"), true);
  assert.equal(matchesGlob("a*b", "a-b"), true);
  assert.equal(matchesGlob("a*b", "a/b"), false);
});

// The release signer handles the private key on whatever machine signs, so a
// change making it disclose that key must never be auto-approvable.
test("the release signer is protected", () => {
  for (const f of ["core/cmd/release-sign/main.go", "scripts/release-sign.sh"]) {
    assert.equal(call({ files: [f] }).action, "comment-sensitive", f);
  }
});
