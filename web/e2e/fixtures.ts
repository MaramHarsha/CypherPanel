import { test as base, expect, type Page } from "@playwright/test";

const EMAIL = process.env.E2E_EMAIL ?? "e2e@example.com";
const PASSWORD = process.env.E2E_PASSWORD ?? "e2e-password-1";

/**
 * Signing in, as the suite's precondition rather than as a step in every test.
 *
 * `networkidle` is never used anywhere in this suite and must not be: the panel
 * holds an SSE connection for live updates, so the network is never idle and
 * every such wait times out at 30s. Wait for an element instead.
 */
async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Email").fill(EMAIL);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: /sign in/i }).click();
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 20_000 });
}

/** A project to hang resources off, made through the UI it is testing. */
async function makeProject(page: Page, name: string) {
  await page.goto("/projects");
  await page.getByRole("button", { name: "New project" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(/name/i).first().fill(name);
  await dialog.getByRole("button", { name: /create/i }).click();
  await expect(dialog).toBeHidden({ timeout: 15_000 });
}

export const test = base.extend<{ signedIn: Page }>({
  signedIn: async ({ page }, use) => {
    await signIn(page);
    await use(page);
  },
});

export { expect, makeProject };
