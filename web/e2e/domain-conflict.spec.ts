import { test, expect, makeProject } from "./fixtures";

/**
 * A hostname already served on this server is refused, and said so before it is
 * refused.
 *
 * THE FAILURE THIS EXISTS TO STOP, reported from a real panel: an operator ran
 * a site on their apex domain, installed a second application into the same
 * project, and the apex started answering 404. Nothing refused the collision.
 * Traefik does not either — it ends up with two routers whose rules are both
 * `Host(...)` for the same hostname, serves one, and the other silently stops.
 * A successful deploy and a dead site, with no error anywhere to read.
 *
 * The API refuses it now (409, `core/applications.checkDomainFree`), and the
 * form says so while you are still filling it in. This asserts the second half,
 * which is the half no backend test can see.
 */
test.describe("a domain already in use", () => {
  test("is called out in the create dialog before submitting", async ({ signedIn: page }) => {
    const taken = `apex-${Date.now().toString(36)}.example.com`;
    await makeProject(page, "domain-conflict");
    // Creating an application navigates to it, so remember where the board is.
    const board = page.url();

    // The first application claims it.
    await page.getByRole("button", { name: /new application/i }).first().click();
    let dialog = page.getByRole("dialog");
    await dialog.getByLabel("Name").fill("first");
    await dialog.getByLabel("Repository", { exact: true }).fill("https://github.com/acme/web");
    await dialog.getByLabel("Domain", { exact: false }).first().fill(taken);
    await dialog.getByRole("button", { name: /deploy/i }).click();
    await expect(dialog).toBeHidden({ timeout: 20_000 });

    // A second one tries the same hostname on the same server.
    await page.goto(board);
    await page.getByRole("button", { name: /new application/i }).first().click();
    dialog = page.getByRole("dialog");
    await dialog.getByLabel("Name").fill("second");
    await dialog.getByLabel("Repository", { exact: true }).fill("https://github.com/acme/other");
    await dialog.getByLabel("Domain", { exact: false }).first().fill(taken);

    // The warning arrives from the server's own list of what it already routes,
    // so it appears while typing rather than after a round trip that fails.
    await expect(dialog.getByRole("alert").filter({ hasText: /already served/i })).toBeVisible({
      timeout: 15_000,
    });
    // And it names the remedy, not the rule.
    await expect(dialog.getByRole("alert").filter({ hasText: /subdomain/i })).toBeVisible();
  });

  test("is refused by the API even if the warning is ignored", async ({ signedIn: page }) => {
    const taken = `apex2-${Date.now().toString(36)}.example.com`;
    await makeProject(page, "domain-conflict-refused");
    const board = page.url();

    await page.getByRole("button", { name: /new application/i }).first().click();
    let dialog = page.getByRole("dialog");
    await dialog.getByLabel("Name").fill("first");
    await dialog.getByLabel("Repository", { exact: true }).fill("https://github.com/acme/web");
    await dialog.getByLabel("Domain", { exact: false }).first().fill(taken);
    await dialog.getByRole("button", { name: /deploy/i }).click();
    await expect(dialog).toBeHidden({ timeout: 20_000 });

    await page.goto(board);
    await page.getByRole("button", { name: /new application/i }).first().click();
    dialog = page.getByRole("dialog");
    await dialog.getByLabel("Name").fill("second");
    await dialog.getByLabel("Repository", { exact: true }).fill("https://github.com/acme/other");
    await dialog.getByLabel("Domain", { exact: false }).first().fill(taken);
    await dialog.getByRole("button", { name: /deploy/i }).click();

    // The dialog stays open carrying the server's refusal: the collision is
    // physical, so the screen's warning is a courtesy and the API is the rule.
    await expect(dialog.getByText(/already served/i).first()).toBeVisible({ timeout: 15_000 });
    await expect(dialog).toBeVisible();
  });
});
