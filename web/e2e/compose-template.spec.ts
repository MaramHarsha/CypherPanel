import { test, expect } from "./fixtures";

/**
 * A compose template can be installed from the catalog.
 *
 * THE FAILURE THIS EXISTS TO STOP, and it was shipped and claimed working:
 * OpenClaw was added to the catalog, its Go tests passed, both parity audits
 * reported no gaps — and there was no sequence of clicks that installed it.
 * `TemplateResources` in the OpenAPI document declared only `databases` and
 * `applications`, so the generated client had no `stacks` field, the screen's
 * copy of the server's "does this need a domain" predicate read applications
 * only, the domain field never rendered, and submitting returned the server's
 * refusal *this template needs a domain* beside a form with no domain control.
 * The card said "0 apps · 0 databases" for something that runs two containers.
 *
 * Every layer was individually correct. Only a browser could see it.
 */
test.describe("a compose template", () => {
  test("shows what it installs and asks for the domain it needs", async ({ signedIn: page }) => {
    await page.goto("/templates");

    // Find it by name rather than by position: the catalog is ~159 entries and
    // sorted by slug, so an index would break the next time one is added.
    const card = page.locator("li").filter({ hasText: /^OpenClaw/ }).first();
    await expect(card).toBeVisible({ timeout: 15_000 });

    // The card must describe what arrives. "0 apps" is what it said while the
    // contract was missing `stacks`.
    await expect(card).not.toContainText("0 apps");
    await expect(card).toContainText(/stack/i);

    await card.getByRole("button", { name: /install/i }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();

    // The summary lists the services, not an empty heading.
    await expect(dialog.getByText(/openclaw/i).first()).toBeVisible();

    // And the domain field is present, because the server says this template
    // needs one. Its absence was the whole bug: the install was refused for a
    // control that was not on the form.
    await expect(dialog.getByLabel("Domain", { exact: false }).first()).toBeVisible({
      timeout: 10_000,
    });
  });
});
