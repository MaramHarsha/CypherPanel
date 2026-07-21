// Role helpers. The server is always the enforcer (404 for non-members, 403
// for rank); the UI's job is to disable what would 403 and say which role it
// needs — never a mystery-disabled button (web-ui-design.md §3).

export type Role = "member" | "admin" | "owner";

const RANK: Record<Role, number> = { member: 0, admin: 1, owner: 2 };

export function atLeast(have: Role | undefined, need: Role): boolean {
  if (!have) return false;
  return RANK[have] >= RANK[need];
}
