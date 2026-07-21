// Bearer-token auth store: memory + localStorage (web-ui-design.md §5). The
// server is the enforcer — this store is plumbing, never a security boundary.

const STORAGE_KEY = "cypher.token";

let token: string | null = null;
try {
  token = localStorage.getItem(STORAGE_KEY);
} catch {
  token = null;
}

const listeners = new Set<() => void>();

export function getToken(): string | null {
  return token;
}

export function setToken(next: string): void {
  token = next;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    // Private-mode storage failure: the in-memory token still works this tab.
  }
  listeners.forEach((l) => l());
}

export function clearToken(): void {
  token = null;
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // ignore
  }
  listeners.forEach((l) => l());
}

export function isAuthenticated(): boolean {
  return token !== null;
}

/** React 18+ useSyncExternalStore contract. */
export function subscribeAuth(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
