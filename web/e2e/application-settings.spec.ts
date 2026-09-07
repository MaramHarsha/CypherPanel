import { test, expect, makeProject } from "./fixtures";

/**
 * Every field the API accepts has a control on the screen that owns it.
 *
 * This file exists because of a specific, repeated failure. An operator
 * deploying a Next.js application hit a health-check failure caused by a port
 * mismatch, went to Application → Settings to fix it, and there was no Port
 * field. They reported it. The same class of gap had already cost them an hour
 * on a deploy key that could not be attached, because that control did not
 * exist either.
 *
 * Both times the API had the field, the database had the column, and the
 * scheduler used it — so no backend test failed, and none could have. The gap
 * was only ever visible to someone looking at the screen.
 *
 * `scripts/api-ui-parity.py` catches this class mechanically now. This catches
 * the half the parity script cannot see: whether the control is actually
 * rendered and usable, rather than whether its field name appears in the file.
 */
test.describe("application settings", () => {
  test.beforeEach(async ({ signedIn: page }) => {
    await makeProject(page, "settings-" + Date.now().toString(36));
    await page.getByRole("button", { name: /new application/i }).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Name").fill("web");
    await dialog.getByLabel("Repository", { exact: true }).fill("https://github.com/acme/web");
    await dialog.getByRole("button", { name: /deploy/i }).click();
    await expect(dialog).toBeHidden({ timeout: 20_000 });
  });

  test("offers the runtime, health and credential controls the API accepts", async ({ signedIn: page }) => {
    await page.getByRole("link", { name: /settings/i }).last().click();
    await expect(page.getByLabel("Repository", { exact: true })).toBeVisible({ timeout: 15_000 });

    // The one that was reported twice. A container listening on 3000 behind a
    // panel configured for 8080 fails its health gate, and this is the only
    // place that can be fixed.
    await expect(page.getByLabel("Port", { exact: true })).toBeVisible();

    // The credential for a private repository. "There is no source? where
    // should i need to keep the deploy key?" — there was nowhere.
    await expect(page.getByLabel("Deploy key", { exact: false }).first()).toBeVisible();

    // The health gate's own tuning, which is what an operator reaches for when
    // an application is slow to start rather than broken.
    await expect(page.getByLabel("Path", { exact: true })).toBeVisible();
  });

  test("saves a changed port", async ({ signedIn: page }) => {
    await page.getByRole("link", { name: /settings/i }).last().click();
    const port = page.getByLabel("Port", { exact: true });
    await expect(port).toBeVisible({ timeout: 15_000 });

    // 3000 is the number the reported failure needed: `next start -p 3000`.
    await port.fill("3000");
    await page.getByRole("button", { name: /save/i }).first().click();

    // Reload rather than trust the optimistic view: the question is whether the
    // value reached the database, not whether the input still holds what was
    // typed into it.
    await page.reload();
    await expect(page.getByLabel("Port", { exact: true })).toHaveValue("3000", { timeout: 15_000 });
  });
});
