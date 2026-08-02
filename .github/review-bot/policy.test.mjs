// node --test .github/review-bot
//
// Every case is a call into the pure decision function with fixtures, so the
// suite needs no token, no network, and no GitHub.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  decide, approvalBody, sensitiveBody, alreadyCommented, shouldRequestReview, matchesGlob,
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
