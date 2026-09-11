// The whole approval decision, as one pure function.
//
// Nothing here touches the network, so every rule is testable with plain
// fixtures and the privileged workflow has no logic worth attacking. The IO
// layer (github.mjs) only fetches; run.mjs only wires the two together.
//
// Every rule FAILS CLOSED: anything missing, unknown, or ambiguous produces
// "skip", never "approve". A bot that approves when it cannot see is worse
// than no bot.

/**
 * Minimal glob: `**` spans separators, `*` does not, everything else literal.
 *
 * A LEADING `**\/` also matches zero directories, so `**\/package.json` covers
 * a root-level package.json as well as nested ones. Without that it required a
 * slash, and a root dependency manifest — exactly the file most worth
 * protecting — was not sensitive while `web/package.json` was.
 */
export function matchesGlob(pattern, path) {
  if (pattern.startsWith("**/")) {
    const rest = pattern.slice(3);
    if (matchesGlob(rest, path)) return true;
  }
  // One pass, so no sentinel is needed to keep `**` from being re-read as two
  // `*`. The previous version parked a NUL byte in the string as that sentinel,
  // which made Git classify this file as binary — and a policy module whose
  // diffs cannot be read on a pull request is the last file that should be
  // unreviewable.
  const rx = pattern
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*\*|\*/g, (m) => (m === "**" ? ".*" : "[^/]*"));
  return new RegExp(`^${rx}$`).test(path);
}

/**
 * Every path a changed-files entry touches.
 *
 * A rename reports the destination as `filename` and the source as
 * `previous_filename`. Reading only the destination meant a pull request could
 * move core/auth/session.go to an unprotected path *while editing it* and see
 * no sensitive hit — the rename itself being the thing worth catching.
 * Accepts plain strings too, so callers may pass either shape.
 */
export function pathsOf(entry) {
  if (typeof entry === "string") return [entry];
  return [entry?.filename, entry?.previous_filename].filter((x) => typeof x === "string" && x);
}

export function sensitiveHits(files, patterns) {
  const hits = new Set();
  for (const entry of files) {
    for (const path of pathsOf(entry)) {
      if (patterns.some((p) => matchesGlob(p, path))) hits.add(path);
    }
  }
  return [...hits].sort();
}

const TERMINAL_OK = "success";

/**
 * decide returns one of:
 *   { action: "approve", actor? }            — actor set when a person asked for it
 *   { action: "comment-sensitive", paths }   — manual review needed, say why
 *   { action: "skip", reason }               — do nothing
 *
 * `triggerSha` is the commit CI actually ran against. It is compared to the
 * PR's current head so an approval can never drift onto a commit nobody tested.
 *
 * `manualApproval` is an authorization recorded by someone entitled to command
 * the bot (see authorizeCommand). It substitutes for the protected-path and
 * trusted-author gates — those exist to stop the bot approving *by itself*,
 * and both are answered by an accountable person saying they have looked. It
 * substitutes for nothing else: the commit must still be the tested one, every
 * required check must still be green on it, and a blocked label still wins.
 */
export function decide(input) {
  const { pr, checks, reviews, files, triggerSha, config, manualApproval } = input ?? {};

  // ── fail closed on anything we could not read ────────────────────────────
  if (!config) return skip("no config");
  if (!pr || typeof pr !== "object") return skip("pull request could not be read");
  if (!Array.isArray(checks)) return skip("check runs could not be read");
  if (!Array.isArray(reviews)) return skip("reviews could not be read");
  if (!Array.isArray(files)) return skip("changed files could not be read");
  if (!triggerSha) return skip("the tested commit sha is unknown");
  if (!pr.head?.sha) return skip("the pull request head sha is unknown");
  if (!pr.user?.login) return skip("the pull request author is unknown");

  // ── state the PR must be in ──────────────────────────────────────────────
  if (pr.state !== "open") return skip(`pull request is ${pr.state}`);
  if (pr.draft) return skip("pull request is a draft");
  if (pr.user.login === config.botLogin) return skip("pull request was opened by the bot");

  // GitHub reports mergeable as null while it is still computing. Unknown is
  // not "fine" — wait for a later run rather than approving blind.
  if (pr.mergeable === false) return skip("pull request has merge conflicts");
  if (pr.mergeable !== true) return skip("mergeability is still unknown");

  // ── the tested commit must be the current one ────────────────────────────
  // This is the rule that stops "CI passed, then someone pushed" from being
  // laundered into an approval of code nobody ran.
  if (triggerSha !== pr.head.sha) {
    return skip(`a newer commit was pushed after CI ran (tested ${short(triggerSha)}, head ${short(pr.head.sha)})`);
  }

  // ── every required check, green, on this exact commit ────────────────────
  const forHead = checks.filter((c) => c.head_sha === pr.head.sha);
  for (const name of config.requiredChecks ?? []) {
    const run = forHead.find((c) => c.name === name);
    if (!run) return skip(`required check "${name}" did not report for this commit`);
    if (run.status !== "completed") return skip(`required check "${name}" is ${run.status}`);
    // skipped/cancelled/timed_out/neutral are all "not a pass". None of this
    // repo's jobs are path-filtered, so a skip is anomalous rather than normal.
    if (run.conclusion !== TERMINAL_OK) return skip(`required check "${name}" concluded ${run.conclusion}`);
  }

  // ── labels the operator uses to say "hands off" ──────────────────────────
  const labels = (pr.labels ?? []).map((l) => (typeof l === "string" ? l : l?.name)).filter(Boolean);
  const blocked = labels.filter((l) => (config.blockedLabels ?? []).includes(l));
  if (blocked.length) return skip(`blocked label: ${blocked.join(", ")}`);

  // An authorization is for ONE commit — the one its author was looking at.
  // Anything pushed afterwards is unreviewed by definition, so it expires
  // rather than carrying forward.
  const manual = manualApprovalFor(manualApproval, pr.head.sha);

  // ── sensitive surface beats author trust, deliberately ───────────────────
  // A trusted author is still a human or a bot that can be compromised, and a
  // dependency bump is the classic supply-chain vector. Checked before the
  // trusted-author gate so the explanation is posted either way.
  const paths = sensitiveHits(files, config.sensitivePaths ?? []);
  if (paths.length && !manual) return { action: "comment-sensitive", paths };

  if (!manual && !(config.trustedAuthors ?? []).includes(pr.user.login)) {
    return skip(`author ${pr.user.login} is not on the trusted list`);
  }

  // ── idempotency: never approve the same commit twice ─────────────────────
  // Only an APPROVED review by the bot ON THIS SHA counts. An approval of an
  // older commit is explicitly not carried forward.
  const already = reviews.some(
    (r) => r.user?.login === config.botLogin && r.state === "APPROVED" && r.commit_id === pr.head.sha,
  );
  // Carries a code as well as a sentence: the IO layer treats this as "done,
  // nothing to do" rather than a refusal, and matching on the prose would make
  // rewording it a behaviour change nobody would expect.
  if (already) return { ...skip("already approved at this commit"), code: "already-approved" };

  return manual ? { action: "approve", actor: manual.actor } : { action: "approve" };
}

/**
 * The authorization that applies to this head commit, or null.
 *
 * Both halves are required: an actor, so the approval can name who is
 * accountable for it, and the exact commit it was given for.
 */
export function manualApprovalFor(authorization, headSha) {
  if (!authorization || typeof authorization !== "object") return null;
  const { sha, actor } = authorization;
  if (typeof sha !== "string" || typeof actor !== "string" || !sha || !actor) return null;
  if (!headSha || sha !== headSha) return null;
  return { actor };
}

/** Commands the bot understands when addressed by name. */
const COMMANDS = new Set(["approve"]);

/**
 * The command in a comment addressed to the bot, or null when there is none.
 *
 * Code spans, fenced blocks, HTML comments and quoted lines are stripped first.
 * All four are how a command ends up in a comment without being *meant* as one:
 * quoting someone who used it, pasting the documentation for it, or a marker
 * the bot itself wrote. Reading those as instructions would make discussing the
 * feature indistinguishable from using it.
 */
export function parseCommand(body, botLogin) {
  if (typeof body !== "string" || !botLogin) return null;
  const text = body
    .replace(/<!--[\s\S]*?-->/g, " ")
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`[^`\n]*`/g, " ")
    .split("\n")
    .filter((line) => !/^\s*>/.test(line))
    .join("\n");
  // Case-insensitive, because a GitHub mention is: @CypherPanel-Review-Bot
  // notifies the same account and would otherwise be typed in good faith and
  // silently ignored. The whitespace is what keeps it from matching a longer
  // login that merely starts with this one.
  const m = new RegExp(`@${escapeRegExp(botLogin)}\\s+([A-Za-z][A-Za-z-]*)`, "i").exec(text);
  if (!m) return null;
  const verb = m[1].toLowerCase();
  return COMMANDS.has(verb) ? { verb } : { verb, unknown: true };
}

/**
 * Whether a comment may command the bot. Returns { ok: true, actor } or
 * { ok: false, reason }.
 *
 * Two independent facts must agree, because this repository is public and
 * anyone at all can type the command:
 *
 *   1. The author is on the configured list — the repository's own statement
 *      of who is allowed to speak for it.
 *   2. GitHub's `author_association` for this comment says the same thing.
 *      That field is computed by GitHub from the account's real relationship
 *      to the repository, so it cannot be set by the commenter, and it is what
 *      stops a recycled or lookalike login from inheriting the authority the
 *      list grants to a name.
 *
 * Anything unreadable is a denial: this is the one place where being wrong
 * hands an approval to a stranger.
 */
export function authorizeCommand(comment, config) {
  if (!config) return deny("no config");
  if (!comment || typeof comment !== "object") return deny("comment could not be read");
  const login = comment.user?.login;
  if (typeof login !== "string" || !login) return deny("comment author is unknown");

  const approvers = config.commandApprovers ?? [];
  if (!approvers.some((a) => String(a).toLowerCase() === login.toLowerCase())) {
    return deny(`${login} is not authorized to command the bot`);
  }
  const allowed = config.commandAssociations ?? [];
  if (!allowed.includes(comment.author_association)) {
    return deny(
      `${login} commented as ${comment.author_association ?? "an unknown association"}, not ${allowed.join(" or ")}`,
    );
  }
  return { ok: true, actor: login };
}

/**
 * The authorization the bot has recorded for this pull request, or null.
 *
 * It lives in a comment the *bot* wrote, so the record cannot be forged by
 * anyone who cannot post as the bot — the command that produced it was
 * authorized once, at the moment it was given, and this is only the receipt.
 * Deleting that comment withdraws it.
 */
export function recordedAuthorization(comments, config) {
  if (!Array.isArray(comments) || !config?.authorizationMarker) return null;
  const rx = new RegExp(
    `<!--\\s*${escapeRegExp(config.authorizationMarker)}\\s+sha=([0-9a-fA-F]{7,64})\\s+by=([A-Za-z0-9-]{1,39})\\s*-->`,
  );
  let found = null;
  for (const c of comments) {
    if (c?.user?.login !== config.botLogin) continue;
    const m = rx.exec(c.body ?? "");
    if (m) found = { sha: m[1], actor: m[2] }; // the newest wins
  }
  return found;
}

/**
 * The review body. Describes what was actually checked — nothing more, and it
 * names whoever asked for the approval when a person did.
 */
export function approvalBody(sha, actor) {
  const why = actor
    ? [
        `Approved at the explicit instruction of @${actor}, who commanded \`@cypherpanel-review-bot approve\` on this pull request and is authorized to do so.`,
        "",
        "That instruction stands in for the protected-path check only — a person accountable for this repository has done the manual review it asks for.",
        `Everything else still had to hold: every required CI check passed for commit \`${short(sha)}\`, which is the current head, and no blocked label was present.`,
      ]
    : [
        `All required CI checks passed for commit \`${short(sha)}\`. No blocked labels or protected changes were detected.`,
      ];
  return [
    "Automated policy review completed by cypherpanel-review-bot.",
    "",
    ...why,
    "",
    "This is a CI and policy check, not a semantic review of the code.",
    "This approval does not merge the pull request. Final merging remains manual.",
  ].join("\n");
}

/**
 * The bot's standing verdict on a pull request that needs manual review.
 *
 * It carries the commit it was computed from, because it is rewritten in place
 * on every commit rather than posted again. Posting again would mean one
 * comment per push on a long-running branch, which is how a comment stops being
 * read; leaving the first one alone — the previous behaviour — meant the
 * verdict silently described a commit from days ago, listing files the pull
 * request might no longer touch.
 */
export function sensitiveBody(paths, marker, sha, config) {
  return [
    marker,
    "Automated policy review completed by cypherpanel-review-bot.",
    "",
    `Evaluated at commit \`${short(sha)}\`. This pull request touches protected areas, so it needs manual review before approval:`,
    "",
    ...paths.slice(0, 20).map((p) => `- \`${p}\``),
    paths.length > 20 ? `- …and ${paths.length - 20} more` : "",
    "",
    "CI status is unaffected — this comment only explains why the bot is not approving.",
    "",
    `${approversPhrase(config)} can record that review by commenting \`@cypherpanel-review-bot approve\`. The bot then approves this commit once every required check is green — nobody else's comment does anything.`,
    "",
    "_This comment is rewritten on every commit, so it always describes the one named above._",
  ]
    .filter((l) => l !== "")
    .join("\n");
}

/**
 * What the standing comment becomes once its verdict no longer applies —
 * because the protected paths went away, or because someone reviewed them.
 * Kept in place rather than deleted: the marker is what lets a later commit
 * turn it back into a verdict, and the history is worth reading.
 */
export function resolvedBody(marker, sha, actor) {
  return [
    marker,
    "Automated policy review completed by cypherpanel-review-bot.",
    "",
    actor
      ? `Approved at commit \`${short(sha)}\` on the instruction of @${actor}. The protected paths this comment listed earlier were reviewed by them.`
      : `No protected areas are touched at commit \`${short(sha)}\`, so this pull request no longer needs manual review. The list this comment carried earlier applied to an older commit.`,
  ].join("\n");
}

/**
 * The receipt for an authorized command the bot could not act on yet, and the
 * durable record that makes it act later.
 *
 * The marker pins the authorization to one commit. Approval is driven by CI
 * finishing, so an instruction given while CI is still running would otherwise
 * be lost — and asking for it again after every red-to-green cycle is the kind
 * of ceremony people work around.
 */
export function authorizationBody(config, sha, actor, reason) {
  return [
    `<!-- ${config.authorizationMarker} sha=${sha} by=${actor} -->`,
    "Automated policy review completed by cypherpanel-review-bot.",
    "",
    `@${actor} authorized approval of commit \`${short(sha)}\`, which the bot cannot act on yet: ${reason}.`,
    "",
    "The authorization is recorded and applies to that commit alone. The bot approves as soon as the remaining conditions hold; a new commit expires it, and deleting this comment withdraws it.",
  ].join("\n");
}

/**
 * The bot's own marked comment on a pull request.
 *
 * `null` means it has not posted one; `undefined` means the comment list could
 * not be read, and the caller must then do nothing rather than post a second
 * copy of something that may already be there.
 */
export function findMarkedComment(comments, botLogin, marker) {
  if (!Array.isArray(comments)) return undefined;
  return comments.find((c) => c?.user?.login === botLogin && (c.body ?? "").includes(marker)) ?? null;
}

/** How the standing comment names the people who may command the bot. */
function approversPhrase(config) {
  const approvers = (config?.commandApprovers ?? []).map((a) => `@${a}`);
  return approvers.length ? approvers.join(" or ") : "Nobody";
}

/** Blocked labels currently on the pull request. */
export function blockedLabelsOf(pr, config) {
  const names = (pr?.labels ?? []).map((l) => (typeof l === "string" ? l : l?.name)).filter(Boolean);
  return names.filter((l) => (config?.blockedLabels ?? []).includes(l));
}

/**
 * What to do when labels change on a pull request the bot may have approved.
 *
 * Approving is triggered by CI finishing, and adding a label does not re-run
 * CI — so without this the operator's "hands off" label landed after an
 * approval changed nothing, and that stale approval went on satisfying the
 * one-approval merge gate. The label has to be able to take the approval back.
 *
 * Returns { action: "dismiss", reviewIds, labels } or { action: "none", reason }.
 */
export function decideOnLabels(pr, reviews, config) {
  if (!config) return none("no config");
  if (!pr || typeof pr !== "object") return none("pull request could not be read");
  if (!Array.isArray(reviews)) return none("reviews could not be read");

  const head = pr.head?.sha;
  const labels = blockedLabelsOf(pr, config);

  // Only reviews still counting toward the gate: a DISMISSED one is already
  // taken back, and re-dismissing it would be a no-op that logs noise.
  const active = reviews.filter(
    (r) => r.user?.login === config.botLogin && r.state === "APPROVED" && r.id != null,
  );
  if (!active.length) return none("no active approval by the bot to dismiss");

  if (labels.length) {
    return { action: "dismiss", reviewIds: active.map((r) => r.id), labels, why: "blocked-label" };
  }

  // GitHub keeps an approval when new commits land unless branch protection is
  // set to dismiss stale reviews. Refusing to CREATE a new approval was never
  // enough on its own: the old one went on satisfying the required approval for
  // code it was never given for. Branch protection is the real guard; this is
  // the belt to its braces, for when that box is unticked.
  if (!head) return none("head sha unknown");
  const stale = active.filter((r) => r.commit_id !== head).map((r) => r.id);
  if (stale.length) return { action: "dismiss", reviewIds: stale, labels: [], why: "stale-commit" };

  return none("approval is current and unblocked");
}

export function dismissalMessage(d) {
  if (d?.why === "stale-commit") {
    return "Approval dismissed by cypherpanel-review-bot: a new commit was pushed, so this approval no longer covers the code under review. It will be re-issued if CI passes on the new commit.";
  }
  return `Approval dismissed by cypherpanel-review-bot: blocked label ${(d?.labels ?? []).join(", ")} was added. Re-approval requires the label to be removed and CI to pass again.`;
}

/** Whether to request the bot as reviewer, for the assignment workflow. */
export function shouldRequestReview(pr, existingRequests, botLogin) {
  if (!pr || !pr.user?.login) return false;
  if (pr.state !== "open" || pr.draft) return false;
  if (pr.user.login === botLogin) return false; // GitHub rejects self-review anyway
  if (!Array.isArray(existingRequests)) return false; // fail closed: no duplicates
  return !existingRequests.some((u) => u?.login === botLogin);
}

const escapeRegExp = (s) => String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const short = (s) => (typeof s === "string" ? s.slice(0, 7) : "unknown");
const skip = (reason) => ({ action: "skip", reason });
const none = (reason) => ({ action: "none", reason });
const deny = (reason) => ({ ok: false, reason });
