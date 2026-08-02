// IO for the review bot. All decisions live in policy.mjs; this only fetches,
// then performs the one action that was decided.
//
// It never sees pull-request *code* — only API metadata — and never shells out,
// so no PR-controlled string can reach a command line.
import { readFileSync } from "node:fs";
import {
  decide, approvalBody, sensitiveBody, alreadyCommented, shouldRequestReview,
  decideOnLabels, dismissalMessage,
} from "./policy.mjs";

const API = process.env.GITHUB_API_URL || "https://api.github.com";
const token = process.env.GITHUB_TOKEN;
const [owner, repo] = (process.env.GITHUB_REPOSITORY || "").split("/");
const mode = process.argv[2]; // "assign" | "approve" | "labels"
const prNumber = Number(process.env.PR_NUMBER);
const triggerSha = process.env.TRIGGER_SHA;
const config = JSON.parse(readFileSync(new URL("./config.json", import.meta.url)));

if (!token || !owner || !repo || !Number.isInteger(prNumber)) die("missing GITHUB_TOKEN / repository / PR number");

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
    const pr = await api(`${base}/pulls/${prNumber}`);
    // Check runs are fetched for the PR's own head, not the trigger's, so a
    // race that moved head is caught by decide()'s sha comparison.
    const head = pr?.head?.sha;
    const checks = head ? (await api(`${base}/commits/${head}/check-runs?per_page=100`))?.check_runs ?? null : null;
    const reviews = await all(`${base}/pulls/${prNumber}/reviews`);
    // Whole entries, not just `filename`: a rename's protected SOURCE lives in
    // `previous_filename`, and policy needs both sides to judge it.
    const files = await all(`${base}/pulls/${prNumber}/files`);

    const d = decide({ pr, checks, reviews, files, triggerSha, config });
    log(`decision: ${d.action}${d.reason ? ` — ${d.reason}` : ""}`);

    if (d.action === "approve") {
      // Re-read the pull request and re-run the whole decision against it,
      // immediately before posting. The label guard and this job are in one
      // concurrency group, but a label can still land in the window between the
      // fetch above and this POST — and a label that arrives one second too
      // late to be seen would otherwise be a blocked label with an approval
      // sitting under it. Re-deciding closes that window; checks and files
      // still apply because a changed head sha fails the decision outright.
      const fresh = await api(`${base}/pulls/${prNumber}`);
      const again = decide({ pr: fresh, checks, reviews, files, triggerSha, config });
      if (again.action !== "approve") {
        log(`stood down before approving: ${again.reason ?? again.action}`);
        process.exit(0);
      }
      await api(`${base}/pulls/${prNumber}/reviews`, {
        method: "POST",
        body: JSON.stringify({ event: "APPROVE", commit_id: pr.head.sha, body: approvalBody(pr.head.sha) }),
      });
      log(`approved #${prNumber} at ${pr.head.sha.slice(0, 7)}`);
    } else if (d.action === "comment-sensitive") {
      const comments = await all(`${base}/issues/${prNumber}/comments`);
      if (alreadyCommented(comments, config.botLogin, config.commentMarker)) {
        log("sensitive-change comment already present");
      } else {
        await api(`${base}/issues/${prNumber}/comments`, {
          method: "POST",
          body: JSON.stringify({ body: sensitiveBody(d.paths, config.commentMarker) }),
        });
        log("posted the sensitive-change comment");
      }
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

function log(m) { process.stdout.write(`${m}\n`); }
function die(m) { process.stderr.write(`review-bot: ${m}\n`); process.exit(1); }
