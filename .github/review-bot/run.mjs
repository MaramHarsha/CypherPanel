// IO for the review bot. All decisions live in policy.mjs; this only fetches,
// then performs the one action that was decided.
//
// It never sees pull-request *code* — only API metadata — and never shells out,
// so no PR-controlled string can reach a command line.
import { readFileSync } from "node:fs";
import {
  decide, approvalBody, sensitiveBody, resolvedBody, authorizationBody,
  findMarkedComment, shouldRequestReview, decideOnLabels, dismissalMessage,
  parseCommand, authorizeCommand, recordedAuthorization,
} from "./policy.mjs";

const API = process.env.GITHUB_API_URL || "https://api.github.com";
const token = process.env.GITHUB_TOKEN;
const [owner, repo] = (process.env.GITHUB_REPOSITORY || "").split("/");
const mode = process.argv[2]; // "assign" | "approve" | "labels" | "command"
const prNumber = Number(process.env.PR_NUMBER);
const triggerSha = process.env.TRIGGER_SHA;
// The command's comment is identified by id only. Its body is then read back
// from the API rather than passed through the workflow, so no string a
// stranger wrote is ever interpolated into anything.
const commentID = Number(process.env.COMMENT_ID);
const config = JSON.parse(readFileSync(new URL("./config.json", import.meta.url)));

if (!token || !owner || !repo || !Number.isInteger(prNumber)) die("missing GITHUB_TOKEN / repository / PR number");
if (mode === "command" && !Number.isInteger(commentID)) die("missing COMMENT_ID");

async function api(path, init) {
  const res = await fetch(`${API}${path}`, {
    ...init,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "x-github-api-version": "2022-11-28",
      ...(init?.body ? { "content-type": "application/json" } : {}),
      ...init?.headers,
    },
  });
  if (!res.ok) throw new Error(`${init?.method ?? "GET"} ${path} -> ${res.status}`);
  return res.status === 204 ? null : res.json();
}

/**
 * Paginates, returning null on any failure OR on truncation, so callers fail
 * closed. Returning the first N pages of an oversized result would have let a
 * protected file sitting on a later page go unseen, and decide() would then
 * approve from data it did not know was incomplete.
 */
const PAGE_CAP = 10; // 1000 items
async function all(path) {
  const out = [];
  for (let page = 1; page <= PAGE_CAP; page++) {
    const batch = await api(`${path}${path.includes("?") ? "&" : "?"}per_page=100&page=${page}`);
    if (!Array.isArray(batch)) return null;
    out.push(...batch);
    if (batch.length < 100) return out; // short page: the list is complete
  }
  return null; // hit the cap with a full page — cannot prove we saw everything
}

const base = `/repos/${owner}/${repo}`;

try {
  if (mode === "assign") {
    const pr = await api(`${base}/pulls/${prNumber}`);
    const requested = pr?.requested_reviewers;
    if (!shouldRequestReview(pr, requested, config.botLogin)) {
      log(`no reviewer request needed for #${prNumber}`);
    } else {
      await api(`${base}/pulls/${prNumber}/requested_reviewers`, {
        method: "POST",
        body: JSON.stringify({ reviewers: [config.botLogin] }),
      });
      log(`requested review from ${config.botLogin} on #${prNumber}`);
    }
  } else if (mode === "approve") {
    const comments = await all(`${base}/issues/${prNumber}/comments`);
    await evaluate(triggerSha, recordedAuthorization(comments, config), comments);
  } else if (mode === "command") {
    // Everything about the command comes from the API, including the body.
    const comment = await api(`${base}/issues/comments/${commentID}`);
    const command = parseCommand(comment?.body, config.botLogin);
    if (!command || command.unknown) {
      log(`no command for me in comment ${commentID}${command ? ` (unknown verb "${command.verb}")` : ""}`);
      process.exit(0);
    }
    // Deliberately silent when refused. This repository is public, so anyone
    // can type the command; replying would turn that into a way to make the
    // bot post on demand, and the refusal is of no interest to anyone but the
    // person who tried.
    const auth = authorizeCommand(comment, config);
    if (!auth.ok) {
      log(`ignoring "${command.verb}": ${auth.reason}`);
      process.exit(0);
    }
    log(`${auth.actor} commanded "${command.verb}"`);

    // Head is read here and the authorization pinned to it, rather than to
    // whatever head turns out to be inside evaluate(). If a commit lands in
    // between, that read-then-pin makes the decision fail on the sha
    // comparison — the alternative would authorize a commit the author never
    // saw, which is the one thing an authorization must not do.
    const pr = await api(`${base}/pulls/${prNumber}`);
    const head = pr?.head?.sha;
    if (!head) die("the pull request head sha is unknown");

    const comments = await all(`${base}/issues/${prNumber}/comments`);
    const d = await evaluate(head, { sha: head, actor: auth.actor }, comments);
    const settled = d.action === "approve" || d.code === "already-approved";
    await react(commentID, settled ? "+1" : "eyes");

    if (!settled) {
      // Record it so the CI-driven path can act on it later: approval is
      // triggered by CI finishing, and an instruction given while CI is still
      // running would otherwise be dropped on the floor.
      await api(`${base}/issues/${prNumber}/comments`, {
        method: "POST",
        body: JSON.stringify({ body: authorizationBody(config, head, auth.actor, d.reason ?? d.action) }),
      });
      log(`recorded ${auth.actor}'s authorization for ${head.slice(0, 7)}`);
    }
  } else if (mode === "labels") {
    // Approving is triggered by CI finishing, and labelling does not re-run
    // CI — so a "hands off" label added after an approval had no effect and the
    // stale approval kept satisfying the merge gate. This takes it back.
    const pr = await api(`${base}/pulls/${prNumber}`);
    const reviews = await all(`${base}/pulls/${prNumber}/reviews`);
    const d = decideOnLabels(pr, reviews, config);
    log(`label decision: ${d.action}${d.reason ? ` — ${d.reason}` : ""}`);
    if (d.action === "dismiss") {
      for (const id of d.reviewIds) {
        await api(`${base}/pulls/${prNumber}/reviews/${id}/dismissals`, {
          method: "PUT",
          body: JSON.stringify({ message: dismissalMessage(d), event: "DISMISS" }),
        });
        log(`dismissed review ${id} on #${prNumber}`);
      }
    }
  } else {
    die(`unknown mode ${mode}`);
  }
} catch (err) {
  // Fail closed and loudly: never let an API problem become an approval.
  die(err instanceof Error ? err.message : String(err));
}

/**
 * One evaluation of the pull request, and whatever it calls for.
 *
 * Shared by the CI-driven path and the command path, so there is exactly one
 * implementation of "may this be approved" and one of "what does the standing
 * comment say". `testedSha` is the commit CI actually ran against.
 */
async function evaluate(testedSha, manualApproval, comments) {
  const pr = await api(`${base}/pulls/${prNumber}`);
  // Check runs are fetched for the PR's own head, not the trigger's, so a
  // race that moved head is caught by decide()'s sha comparison.
  const head = pr?.head?.sha;
  const checks = head ? (await api(`${base}/commits/${head}/check-runs?per_page=100`))?.check_runs ?? null : null;
  const reviews = await all(`${base}/pulls/${prNumber}/reviews`);
  // Whole entries, not just `filename`: a rename's protected SOURCE lives in
  // `previous_filename`, and policy needs both sides to judge it.
  const files = await all(`${base}/pulls/${prNumber}/files`);

  const input = { pr, checks, reviews, files, triggerSha: testedSha, config, manualApproval };
  const d = decide(input);
  log(`decision: ${d.action}${d.reason ? ` — ${d.reason}` : ""}${d.actor ? ` (authorized by ${d.actor})` : ""}`);

  if (d.action === "approve") {
    // Re-read the pull request and re-run the whole decision against it,
    // immediately before posting. The label guard and this job are in one
    // concurrency group, but a label can still land in the window between the
    // fetch above and this POST — and a label that arrives one second too
    // late to be seen would otherwise be a blocked label with an approval
    // sitting under it. Re-deciding closes that window; checks and files
    // still apply because a changed head sha fails the decision outright.
    const fresh = await api(`${base}/pulls/${prNumber}`);
    const again = decide({ ...input, pr: fresh });
    if (again.action !== "approve") {
      log(`stood down before approving: ${again.reason ?? again.action}`);
      return again;
    }
    await api(`${base}/pulls/${prNumber}/reviews`, {
      method: "POST",
      body: JSON.stringify({ event: "APPROVE", commit_id: head, body: approvalBody(head, d.actor) }),
    });
    log(`approved #${prNumber} at ${head.slice(0, 7)}`);
    // Only if one is already there: an approved pull request that never needed
    // manual review does not need the bot to say so.
    await reconcileComment(comments, resolvedBody(config.commentMarker, head, d.actor), { create: false });
  } else if (d.action === "comment-sensitive") {
    await reconcileComment(comments, sensitiveBody(d.paths, config.commentMarker, head, config), { create: true });
  }
  return d;
}

/**
 * Bring the bot's standing comment in line with the current verdict: post it
 * when it is missing and wanted, edit it when it has drifted, otherwise leave
 * it alone.
 *
 * Editing rather than posting again is what lets the verdict track every
 * commit without turning a long-lived branch into a wall of near-identical
 * comments. It also makes the write idempotent by content, which is what keeps
 * the two triggers (CI and Integration both finishing) from fighting over it.
 */
async function reconcileComment(comments, body, { create }) {
  const existing = findMarkedComment(comments, config.botLogin, config.commentMarker);
  if (existing === undefined) {
    log("comments could not be read — leaving the standing comment alone");
    return;
  }
  if (existing === null) {
    if (!create) return;
    await api(`${base}/issues/${prNumber}/comments`, { method: "POST", body: JSON.stringify({ body }) });
    log("posted the standing review comment");
    return;
  }
  if ((existing.body ?? "") === body) {
    log("the standing review comment is already current");
    return;
  }
  await api(`${base}/issues/comments/${existing.id}`, { method: "PATCH", body: JSON.stringify({ body }) });
  log(`refreshed the standing review comment for ${prNumber}`);
}

/** Acknowledge a command on the comment itself. Never worth failing a run. */
async function react(id, content) {
  try {
    await api(`${base}/issues/comments/${id}/reactions`, { method: "POST", body: JSON.stringify({ content }) });
  } catch (err) {
    log(`could not react to comment ${id}: ${err instanceof Error ? err.message : String(err)}`);
  }
}

function log(m) { process.stdout.write(`${m}\n`); }
function die(m) { process.stderr.write(`review-bot: ${m}\n`); process.exit(1); }
