// Typed API client for CypherCore. Paths/schemas come from the generated
// lib/api-types.ts (npm run gen:api) — never hand-write endpoint shapes.
//
// Token model: short-lived access token kept in memory only; single-use
// refresh token persisted in localStorage and rotated on every refresh.
// TODO(hardening): move refresh token to an httpOnly cookie once the core
// serves the UI behind the same origin in production.

import type { components } from "./api-types";

export type ServerInfo = components["schemas"]["api.serverResponse"];
export type TokenPair = components["schemas"]["api.tokenResponse"];

const REFRESH_KEY = "cypher.refresh_token";

let accessToken: string | null = null;

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function rawFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Content-Type", "application/json");
  if (accessToken) headers.set("Authorization", `Bearer ${accessToken}`);
  return fetch(path, { ...init, headers });
}

async function tryRefresh(): Promise<boolean> {
  const refresh = localStorage.getItem(REFRESH_KEY);
  if (!refresh) return false;
  const res = await fetch("/api/v1/auth/refresh", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ refresh_token: refresh }),
  });
  if (!res.ok) {
    localStorage.removeItem(REFRESH_KEY);
    accessToken = null;
    return false;
  }
  const tokens = (await res.json()) as TokenPair;
  accessToken = tokens.access_token ?? null;
  if (tokens.refresh_token) localStorage.setItem(REFRESH_KEY, tokens.refresh_token);
  return true;
}

/** Authenticated fetch with automatic one-shot refresh on 401. */
export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  let res = await rawFetch(path, init);
  if (res.status === 401 && (await tryRefresh())) {
    res = await rawFetch(path, init);
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new ApiError(res.status, body.error ?? `request failed (${res.status})`);
  }
  return (await res.json()) as T;
}

export async function login(username: string, password: string): Promise<void> {
  const res = await fetch("/api/v1/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new ApiError(res.status, body.error ?? "login failed");
  }
  const tokens = (await res.json()) as TokenPair;
  accessToken = tokens.access_token ?? null;
  if (tokens.refresh_token) localStorage.setItem(REFRESH_KEY, tokens.refresh_token);
}

export async function logout(): Promise<void> {
  const refresh = localStorage.getItem(REFRESH_KEY);
  try {
    await rawFetch("/api/v1/auth/logout", {
      method: "POST",
      body: JSON.stringify({ refresh_token: refresh }),
    });
  } finally {
    accessToken = null;
    localStorage.removeItem(REFRESH_KEY);
  }
}

/** True once a session exists (access token in memory or refresh available). */
export function hasSession(): boolean {
  return accessToken !== null || localStorage.getItem(REFRESH_KEY) !== null;
}

export function listServers(): Promise<ServerInfo[]> {
  return apiFetch<ServerInfo[]>("/api/v1/admin/servers");
}

export interface Me {
  id: string;
  username: string;
  email: string;
  role: string;
}

export function getMe(): Promise<Me> {
  return apiFetch<Me>("/api/v1/me");
}
