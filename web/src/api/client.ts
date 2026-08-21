// The one fetch wrapper behind the generated client (orval mutator). All API
// traffic flows through here: bearer auth, the {"error": string} envelope, and
// the global 401 → login redirect (web-ui-design.md §5).
import { clearToken, getToken } from "@/lib/auth";

/** The API's error envelope, written for humans (render `error` verbatim). */
export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

function redirectToLogin(): void {
  const here = window.location.pathname + window.location.search;
  const ret = here === "/" || here.startsWith("/login") ? "" : `?return=${encodeURIComponent(here)}`;
  window.location.assign(`/login${ret}`);
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

  const res = await fetch(url, { ...init, headers });
  if (res.status === 401) {
    clearToken();
    redirectToLogin();
    throw new ApiError(401, "Session expired — sign in again");
  }
  // A missing image is an answer, not a failure: it means "no photo".
  if (res.status === 404) return null;
  if (!res.ok) throw new ApiError(res.status, res.statusText || `Request failed (${res.status})`);
  return res.blob();
}

export async function apiFetch<T>(url: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init?.body != null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await fetch(url, { ...init, headers });

  if (res.status === 401 && !url.includes("/auth/login")) {
    clearToken();
    redirectToLogin();
    throw new ApiError(401, "Session expired — sign in again");
  }

  if (!res.ok) {
    let message = res.statusText || `Request failed (${res.status})`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Non-JSON error body — keep the status text.
    }
    throw new ApiError(res.status, message);
  }

  if (res.status === 204 || res.headers.get("Content-Length") === "0") {
    return undefined as T;
  }
  const text = await res.text();
  if (text === "") return undefined as T;
  return JSON.parse(text) as T;
}
