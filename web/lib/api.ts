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
export type PackageInfo = components["schemas"]["api.packageResponse"];
export type PackageLimits = components["schemas"]["api.packageLimits"];
export type AccountInfo = components["schemas"]["api.accountResponse"];
export type CreateAccountRequest = components["schemas"]["api.createAccountRequest"];
export type ResellerInfo = components["schemas"]["api.resellerResponse"];
export type CreateResellerRequest = components["schemas"]["api.createResellerRequest"];

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

// The refresh token is single-use (rotated server-side on every refresh), so
// concurrent 401s — e.g. every query on a page firing at once after a hard
// reload with no in-memory access token — must share one refresh call rather
// than each independently redeeming it: only the first would succeed and the
// rest would burn the already-rotated token and fail.
let refreshPromise: Promise<boolean> | null = null;

function tryRefresh(): Promise<boolean> {
  if (!refreshPromise) {
    refreshPromise = doRefresh().finally(() => {
      refreshPromise = null;
    });
  }
  return refreshPromise;
}

async function doRefresh(): Promise<boolean> {
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

/** Current in-memory access token, refreshing first if absent. Used to build
 *  the WebSocket URL for the terminal (a WS can't carry an auth header). */
export async function accessTokenForWS(): Promise<string | null> {
  if (accessToken) return accessToken;
  if (await tryRefresh()) return accessToken;
  return null;
}

/** Lists the fleet. A non-empty region scopes the result to that region. */
export function listServers(region = ""): Promise<ServerInfo[]> {
  const q = region ? `?region=${encodeURIComponent(region)}` : "";
  return apiFetch<ServerInfo[]>(`/api/v1/admin/servers${q}`);
}

export function getServer(id: string): Promise<ServerInfo> {
  return apiFetch<ServerInfo>(`/api/v1/admin/servers/${id}`);
}

export function deleteServer(id: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/servers/${id}`, { method: "DELETE" });
}

export function managePHPRuntime(
  serverId: string,
  version: string,
  action: "install" | "uninstall",
): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/servers/${serverId}/php`, {
    method: "POST",
    body: JSON.stringify({ version, action }),
  });
}

export type ServiceAction = "start" | "stop" | "restart" | "reload";

export function controlService(
  serverId: string,
  service: string,
  action: ServiceAction,
): Promise<void> {
  return apiFetch<void>(
    `/api/v1/admin/servers/${serverId}/services/${service}/control`,
    { method: "POST", body: JSON.stringify({ action }) },
  );
}

export function listPackages(): Promise<PackageInfo[]> {
  return apiFetch<PackageInfo[]>("/api/v1/admin/packages");
}

export function createPackage(name: string, limits: PackageLimits): Promise<PackageInfo> {
  return apiFetch<PackageInfo>("/api/v1/admin/packages", {
    method: "POST",
    body: JSON.stringify({ name, limits }),
  });
}

export function deletePackage(id: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/packages/${id}`, { method: "DELETE" });
}

export function listAccounts(): Promise<AccountInfo[]> {
  return apiFetch<AccountInfo[]>("/api/v1/admin/accounts");
}

export function createAccount(req: CreateAccountRequest): Promise<AccountInfo> {
  return apiFetch<AccountInfo>("/api/v1/admin/accounts", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

export function accountAction(id: string, action: "suspend" | "unsuspend" | "terminate"): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${id}/${action}`, { method: "POST" });
}

export function phpIniKeys(): Promise<string[]> {
  return apiFetch<string[]>("/api/v1/admin/php/ini-keys");
}

export function updatePHPSettings(id: string, settings: Record<string, string>): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${id}/php-settings`, {
    method: "PATCH",
    body: JSON.stringify({ settings }),
  });
}

export function issueSSL(id: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${id}/ssl`, { method: "POST" });
}

export function phpVersions(): Promise<string[]> {
  return apiFetch<string[]>("/api/v1/admin/php/versions");
}

export function changePHPVersion(id: string, version: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${id}/php-version`, {
    method: "PATCH",
    body: JSON.stringify({ version }),
  });
}

export type DatabaseInfo = components["schemas"]["api.databaseResponse"];

export function listDatabases(accountId: string): Promise<DatabaseInfo[]> {
  return apiFetch<DatabaseInfo[]>(`/api/v1/admin/accounts/${accountId}/databases`);
}

export function createDatabase(accountId: string, name: string): Promise<DatabaseInfo> {
  return apiFetch<DatabaseInfo>(`/api/v1/admin/accounts/${accountId}/databases`, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
}

export function deleteDatabase(accountId: string, dbId: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${accountId}/databases/${dbId}`, {
    method: "DELETE",
  });
}

export interface DBCredentials {
  username: string;
  host: string;
  password: string;
}

export function revealDBPassword(accountId: string, dbId: string): Promise<DBCredentials> {
  return apiFetch<DBCredentials>(
    `/api/v1/admin/accounts/${accountId}/databases/${dbId}/password`,
  );
}

export interface AdminerHandoff {
  url: string;
  driver: string;
  server: string;
  username: string;
  password: string;
  db: string;
}

export function adminerHandoff(accountId: string, dbId: string): Promise<AdminerHandoff> {
  return apiFetch<AdminerHandoff>(
    `/api/v1/admin/accounts/${accountId}/databases/${dbId}/adminer`,
  );
}

export interface FTPInfo {
  id: string;
  username: string;
  home_dir: string;
  status: string;
  created_at: string;
}

export function listFTP(accountId: string): Promise<FTPInfo[]> {
  return apiFetch<FTPInfo[]>(`/api/v1/admin/accounts/${accountId}/ftp`);
}

export function createFTP(accountId: string, name: string): Promise<FTPInfo> {
  return apiFetch<FTPInfo>(`/api/v1/admin/accounts/${accountId}/ftp`, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
}

export function deleteFTP(accountId: string, ftpId: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${accountId}/ftp/${ftpId}`, {
    method: "DELETE",
  });
}

export interface FTPCredentials {
  username: string;
  home_dir: string;
  password: string;
}

export function revealFTPPassword(accountId: string, ftpId: string): Promise<FTPCredentials> {
  return apiFetch<FTPCredentials>(
    `/api/v1/admin/accounts/${accountId}/ftp/${ftpId}/password`,
  );
}

export interface FSEntry {
  name: string;
  is_dir: boolean;
  size: number;
  mode: string;
  mod_time: string;
}

const fmBase = (id: string) => `/api/v1/admin/accounts/${id}`;

export function fmList(accountId: string, path: string): Promise<{ entries: FSEntry[] }> {
  return apiFetch<{ entries: FSEntry[] }>(
    `${fmBase(accountId)}/files?path=${encodeURIComponent(path)}`,
  );
}

export function fmRead(accountId: string, path: string): Promise<{ content: string }> {
  return apiFetch<{ content: string }>(
    `${fmBase(accountId)}/file?path=${encodeURIComponent(path)}`,
  );
}

export function fmWrite(accountId: string, path: string, content: string): Promise<void> {
  return apiFetch<void>(`${fmBase(accountId)}/file`, {
    method: "PUT",
    body: JSON.stringify({ path, content }),
  });
}

export function fmMkdir(accountId: string, path: string): Promise<void> {
  return apiFetch<void>(`${fmBase(accountId)}/files/dir`, {
    method: "POST",
    body: JSON.stringify({ path }),
  });
}

export function fmDelete(accountId: string, path: string): Promise<void> {
  return apiFetch<void>(`${fmBase(accountId)}/files?path=${encodeURIComponent(path)}`, {
    method: "DELETE",
  });
}

export function fmRename(accountId: string, path: string, newPath: string): Promise<void> {
  return apiFetch<void>(`${fmBase(accountId)}/files/rename`, {
    method: "POST",
    body: JSON.stringify({ path, new_path: newPath }),
  });
}

export interface DNSRecord {
  name: string;
  type: string;
  ttl: number;
  contents: string[];
}

export function dnsList(accountId: string): Promise<DNSRecord[]> {
  return apiFetch<DNSRecord[]>(`/api/v1/admin/accounts/${accountId}/dns`);
}

export function dnsRecordTypes(): Promise<string[]> {
  return apiFetch<string[]>("/api/v1/admin/dns/record-types");
}

export function dnsUpsert(accountId: string, record: DNSRecord): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${accountId}/dns`, {
    method: "POST",
    body: JSON.stringify(record),
  });
}

export function dnsDelete(accountId: string, name: string, type: string): Promise<void> {
  return apiFetch<void>(
    `/api/v1/admin/accounts/${accountId}/dns?name=${encodeURIComponent(name)}&type=${encodeURIComponent(type)}`,
    { method: "DELETE" },
  );
}

export function listResellers(): Promise<ResellerInfo[]> {
  return apiFetch<ResellerInfo[]>("/api/v1/admin/resellers");
}

export function createReseller(req: CreateResellerRequest): Promise<ResellerInfo> {
  return apiFetch<ResellerInfo>("/api/v1/admin/resellers", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

export interface MailInfo {
  id: string;
  address: string;
  quota_mb: number;
  status: string;
  created_at: string;
}

export function listMail(accountId: string): Promise<MailInfo[]> {
  return apiFetch<MailInfo[]>(`/api/v1/admin/accounts/${accountId}/mail`);
}

export function createMail(
  accountId: string,
  body: { local_part: string; password: string; quota_mb: number },
): Promise<MailInfo> {
  return apiFetch<MailInfo>(`/api/v1/admin/accounts/${accountId}/mail`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function deleteMail(accountId: string, mailId: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${accountId}/mail/${mailId}`, {
    method: "DELETE",
  });
}

export function getCron(accountId: string): Promise<{ content: string }> {
  return apiFetch<{ content: string }>(`/api/v1/admin/accounts/${accountId}/cron`);
}

export function setCron(accountId: string, content: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/accounts/${accountId}/cron`, {
    method: "PUT",
    body: JSON.stringify({ content }),
  });
}

export interface AuditRecord {
  id: string;
  actor_name: string;
  actor_role: string;
  action: string;
  target_type: string;
  target_id: string;
  detail: Record<string, unknown> | null;
  ip_address: string;
  created_at: string;
}

export function listAudit(action = "", limit = 100, offset = 0): Promise<AuditRecord[]> {
  const q = new URLSearchParams({ action, limit: String(limit), offset: String(offset) });
  return apiFetch<AuditRecord[]>(`/api/v1/admin/audit?${q.toString()}`);
}

export interface BackupDestination {
  id: string;
  name: string;
  kind: "local" | "s3" | "sftp" | "rest";
  repository: string;
  schedule: "off" | "daily" | "weekly";
  retention_daily: number;
  retention_weekly: number;
  retention_monthly: number;
  last_run_at?: string;
  created_at: string;
}

// Credentials are write-only: the API never returns them for any role, so
// there is deliberately no field for them on BackupDestination.
export interface CreateDestinationRequest {
  name: string;
  kind: BackupDestination["kind"];
  repository: string;
  password: string;
  env?: Record<string, string>;
  schedule?: BackupDestination["schedule"];
  retention_daily?: number;
  retention_weekly?: number;
  retention_monthly?: number;
}

export interface AccountBackup {
  id: string;
  account_id: string;
  destination_id: string;
  task_id?: string;
  snapshot_id?: string;
  kind: "manual" | "scheduled" | "restore";
  status: "running" | "completed" | "failed";
  size_bytes: number;
  error?: string;
  started_at: string;
  completed_at?: string;
}

export function listDestinations(): Promise<BackupDestination[]> {
  return apiFetch<BackupDestination[]>("/api/v1/admin/backup/destinations");
}

export function createDestination(req: CreateDestinationRequest): Promise<BackupDestination> {
  return apiFetch<BackupDestination>("/api/v1/admin/backup/destinations", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

export function updateDestination(
  id: string,
  body: {
    schedule: BackupDestination["schedule"];
    retention_daily: number;
    retention_weekly: number;
    retention_monthly: number;
  },
): Promise<BackupDestination> {
  return apiFetch<BackupDestination>(`/api/v1/admin/backup/destinations/${id}`, {
    method: "PATCH",
    body: JSON.stringify(body),
  });
}

export function deleteDestination(id: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/backup/destinations/${id}`, { method: "DELETE" });
}

export function listBackups(accountId: string): Promise<AccountBackup[]> {
  return apiFetch<AccountBackup[]>(`/api/v1/admin/accounts/${accountId}/backups`);
}

export function runBackup(accountId: string, destinationId: string): Promise<AccountBackup> {
  return apiFetch<AccountBackup>(`/api/v1/admin/accounts/${accountId}/backups`, {
    method: "POST",
    body: JSON.stringify({ destination_id: destinationId }),
  });
}

export function restoreBackup(
  accountId: string,
  backupId: string,
  snapshotId: string,
  target: "" | "home",
): Promise<AccountBackup> {
  return apiFetch<AccountBackup>(
    `/api/v1/admin/accounts/${accountId}/backups/${backupId}/restore`,
    { method: "POST", body: JSON.stringify({ snapshot_id: snapshotId, target }) },
  );
}

export interface Webhook {
  id: string;
  name: string;
  url: string;
  events: string[];
  active: boolean;
  created_at: string;
  /** Only present in the create response — the signing key is never shown again. */
  secret?: string;
}

export interface WebhookDelivery {
  id: string;
  webhook_id: string;
  webhook_name: string;
  event_id: string;
  subject: string;
  payload: unknown;
  status: "pending" | "delivered" | "failed" | "dead";
  attempts: number;
  response_status: number;
  error?: string;
  created_at: string;
  delivered_at?: string;
}

export function listWebhooks(): Promise<Webhook[]> {
  return apiFetch<Webhook[]>("/api/v1/admin/webhooks");
}

export function createWebhook(body: {
  name: string;
  url: string;
  events: string[];
}): Promise<Webhook> {
  return apiFetch<Webhook>("/api/v1/admin/webhooks", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function setWebhookActive(id: string, active: boolean): Promise<Webhook> {
  return apiFetch<Webhook>(`/api/v1/admin/webhooks/${id}`, {
    method: "PATCH",
    body: JSON.stringify({ active }),
  });
}

export function deleteWebhook(id: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/webhooks/${id}`, { method: "DELETE" });
}

export function webhookEventSubjects(): Promise<string[]> {
  return apiFetch<string[]>("/api/v1/admin/webhooks/event-subjects");
}

export function listWebhookDeliveries(webhookId = "", limit = 50): Promise<WebhookDelivery[]> {
  const q = new URLSearchParams({ webhook_id: webhookId, limit: String(limit) });
  return apiFetch<WebhookDelivery[]>(`/api/v1/admin/webhooks/deliveries?${q.toString()}`);
}

export function redeliverWebhook(deliveryId: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/webhooks/deliveries/${deliveryId}/redeliver`, {
    method: "POST",
  });
}

export interface PluginManifest {
  api_version: string;
  name: string;
  version: string;
  kind: string;
  description?: string;
  author?: string;
  ui?: {
    sidebar?: { label: string; path: string; icon: string }[];
    dashboard_cards?: { id: string; title: string }[];
    settings_pages?: { label: string; path: string }[];
  };
  events?: string[];
  permissions?: string[];
}

export interface PluginInfo {
  name: string;
  version: string;
  kind: string;
  enabled: boolean;
  manifest?: PluginManifest;
  installed_at: string;
}

export interface PluginSurface {
  plugin: string;
  sidebar: { label: string; path: string; icon: string }[];
  dashboard_cards: { id: string; title: string }[];
  settings_pages: { label: string; path: string }[];
}

export function listPlugins(): Promise<PluginInfo[]> {
  return apiFetch<PluginInfo[]>("/api/v1/admin/plugins");
}

export function installPlugin(manifest: string): Promise<PluginInfo> {
  return apiFetch<PluginInfo>("/api/v1/admin/plugins", {
    method: "POST",
    body: JSON.stringify({ manifest }),
  });
}

export function setPluginEnabled(name: string, enabled: boolean): Promise<PluginInfo> {
  return apiFetch<PluginInfo>(`/api/v1/admin/plugins/${name}`, {
    method: "PATCH",
    body: JSON.stringify({ enabled }),
  });
}

export function uninstallPlugin(name: string): Promise<void> {
  return apiFetch<void>(`/api/v1/admin/plugins/${name}`, { method: "DELETE" });
}

export function pluginSurfaces(): Promise<PluginSurface[]> {
  return apiFetch<PluginSurface[]>("/api/v1/admin/plugins/surfaces");
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
