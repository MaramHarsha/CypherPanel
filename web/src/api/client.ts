// The one fetch wrapper behind the generated client (orval mutator). All API
// traffic flows through here: bearer auth, the {"error": string} envelope, and
// the global 401 → login redirect (web-ui-design.md §5).
//
// It is also where a failure gets its shape. The error pages (8a–8e) and the
// toast layer (10c) both need to know *what kind* of failure this was — a
// refusal, a panel fault, or no answer at all — and which request it was, so
// the operator can be told the route that failed and copy it into an issue.
// cypherd stamps no request or trace id on its responses (core/api/rest has no
// request-id middleware and the Error envelope is `{error}` alone), so nothing
// here pretends to carry one.
import { clearToken, getToken } from "@/lib/auth";

interface RequestMeta {
  /** HTTP method of the request that failed. */
  method: string;
  /** Path only — never the query string, which can carry search terms. */
  path: string;
}

/** The API's error envelope, written for humans (render `error` verbatim). */
export class ApiError extends Error {
  readonly status: number;
  /**
   * The parsed error body, when there was one. Most callers want `message` and
   * nothing else — but some 4xx responses are a QUESTION rather than a refusal
   * and carry the choices needed to answer it (connecting a DNS provider whose
   * token reaches several Cloudflare accounts is the first). Dropping the body
   * would send the operator elsewhere to find something the panel already knew.
   */
  readonly body?: unknown;
  readonly method: string;
  readonly path: string;
  /**
   * Seconds until the server will take the request again, read from a standard
   * `Retry-After` header when one is sent. cypherd's login throttle (8e) sends
   * none today, so this is undefined for it and the throttled page says
   * "in a moment" rather than inventing a countdown.
   */
  readonly retryAfterSeconds?: number;

  constructor(
    status: number,
    message: string,
    body?: unknown,
    meta: Partial<RequestMeta> & { retryAfterSeconds?: number } = {},
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
    this.method = meta.method ?? "GET";
    this.path = meta.path ?? "";
    this.retryAfterSeconds = meta.retryAfterSeconds;
  }
}

/**
 * No HTTP answer at all: cypherd is down, the network is, or the browser
 * refused the connection. Distinct from ApiError because the remedy is
 * different — nothing about the request was wrong, and the fleet is unaffected
 * (8c) — so the UI must not read it as the panel having *said* something.
 */
export class NetworkError extends Error {
  readonly method: string;
  readonly path: string;
  constructor(meta: RequestMeta, cause: unknown) {
    super("Can't reach the control plane", { cause });
    this.name = "NetworkError";
    this.method = meta.method;
    this.path = meta.path;
  }
}

/**
 * Routes where a 401 is a REJECTED CREDENTIAL rather than an expired session.
 * They are the ones that establish a session in the first place — sign-in, and
 * accepting an invitation for an address that already has an account, which
 * runs the ordinary sign-in path and so can answer 401 for a wrong password or
 * a missing second factor. Sending those to /login would replace the panel's
 * own sentence with "session expired" and navigate away from the form that
 * needs to show it.
 */
function establishesSession(url: string): boolean {
  return url.includes("/auth/login") || /\/invites\/[^/]+\/accept/.test(url);
}

function redirectToLogin(): void {
  const here = window.location.pathname + window.location.search;
  const ret = here === "/" || here.startsWith("/login") ? "" : `?return=${encodeURIComponent(here)}`;
  window.location.assign(`/login${ret}`);
}

function metaOf(url: string, init?: RequestInit): RequestMeta {
  let path = url;
  try {
    path = new URL(url, window.location.origin).pathname;
  } catch {
    // A URL the browser cannot parse is still worth naming as it was written.
  }
  return { method: (init?.method ?? "GET").toUpperCase(), path };
}

/** `Retry-After` is either delta-seconds or an HTTP date; both become seconds. */
function retryAfterOf(res: Response): number | undefined {
  const raw = res.headers.get("Retry-After");
  if (!raw) return undefined;
  if (/^\d+$/.test(raw)) return Number(raw);
  const at = Date.parse(raw);
  if (Number.isNaN(at)) return undefined;
  return Math.max(0, Math.ceil((at - Date.now()) / 1000));
}

/** `POST /api/v1/deployments` — the request a failure belongs to, if known. */
export function requestLineOf(err: unknown): string | undefined {
  if ((err instanceof ApiError || err instanceof NetworkError) && err.path) return `${err.method} ${err.path}`;
  return undefined;
}

/**
 * The lines an operator would paste into an issue, and nothing more: the
 * route, what the server answered, and its own words. No hostnames, no env
 * vars, no logs, no IPs (13ai) — every value here already left the server as a
 * response to this browser.
 */
export function faultBundleOf(err: unknown): string {
  const lines: string[] = [];
  const route = requestLineOf(err);
  if (route) lines.push(`route: ${route}`);
  if (err instanceof ApiError) lines.push(`status: ${err.status}`);
  else if (err instanceof NetworkError) lines.push("status: no response");
  const message = err instanceof Error ? err.message : String(err);
  if (message) lines.push(`error: ${message}`);
  return lines.join("\n");
}

async function send(url: string, init: RequestInit | undefined, headers: Headers): Promise<Response> {
  try {
    return await fetch(url, { ...init, headers });
  } catch (cause) {
    throw new NetworkError(metaOf(url, init), cause);
  }
}

/**
 * The same request path, for a response that is bytes rather than JSON.
 *
 * It exists because an authenticated image cannot be an `<img src>`: the URL
 * carries no bearer token, and putting one in a query string would write the
 * credential into history and logs. So the bytes are fetched here and handed to
 * the element as an object URL.
 */
export async function apiBlob(url: string, init?: RequestInit): Promise<Blob | null> {
  const headers = new Headers(init?.headers);
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);

  const res = await send(url, init, headers);
  const meta = metaOf(url, init);
  if (res.status === 401) {
    clearToken();
    redirectToLogin();
    throw new ApiError(401, "Session expired — sign in again", undefined, meta);
  }
  // A missing image is an answer, not a failure: it means "no photo".
  if (res.status === 404) return null;
  if (!res.ok) {
    throw new ApiError(res.status, res.statusText || `Request failed (${res.status})`, undefined, meta);
  }
  return res.blob();
}

export async function apiFetch<T>(url: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init?.body != null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await send(url, init, headers);
  const meta = metaOf(url, init);

  if (res.status === 401 && !establishesSession(url)) {
    clearToken();
    redirectToLogin();
    throw new ApiError(401, "Session expired — sign in again", undefined, meta);
  }

  if (!res.ok) {
    let message = res.statusText || `Request failed (${res.status})`;
    let parsed: unknown;
    try {
      parsed = await res.json();
      const body = parsed as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Non-JSON error body — keep the status text.
    }
    throw new ApiError(res.status, message, parsed, { ...meta, retryAfterSeconds: retryAfterOf(res) });
  }

  if (res.status === 204 || res.headers.get("Content-Length") === "0") {
    return undefined as T;
  }
  const text = await res.text();
  if (text === "") return undefined as T;
  return JSON.parse(text) as T;
}
