import { test, expect, makeProject } from "./fixtures";

/**
 * The repository a user types must be one git can clone.
 *
 * This is the regression an external review found on 2026-09-07: the create
 * dialog suggested `github.com/acme/web`, nothing between the form and the
 * builder looked at the value, and `git clone` reads a schemeless string as a
 * LOCAL DIRECTORY. So the application saved successfully and the build died
 * with `exit status 128` — a status line naming neither the field nor the
 * mistake.
 *
 * No backend test could have caught it: the API accepted the value, which was
 * the bug. It needed someone to type into the form.
 */
test.describe("the repository field", () => {
  test("refuses a value git would read as a local directory", async ({ signedIn: page }) => {
    await makeProject(page, "repo-validation");
    await page.getByRole("button", { name: /new application/i }).first().click();
    const dialog = page.getByRole("dialog");

    await dialog.getByLabel("Name").fill("bad-repo");
    // The shape a first-timer types when nothing tells them otherwise. It has
    // no host, so it can only be a path on the builder.
    await dialog.getByLabel("Repository", { exact: true }).fill("acme/web");
    await dialog.getByRole("button", { name: /deploy/i }).click();

    // Refused at save time, with a sentence that names the accepted forms —
    // rather than accepted here and failed five minutes into a build.
    await expect(dialog.getByText(/must be a git remote/i)).toBeVisible({ timeout: 15_000 });
    await expect(dialog).toBeVisible();
  });

  test("suggests a form that actually clones", async ({ signedIn: page }) => {
    await makeProject(page, "repo-placeholder");
    await page.getByRole("button", { name: /new application/i }).first().click();
    const dialog = page.getByRole("dialog");

    const repo = dialog.getByLabel("Repository", { exact: true });
    // The placeholder is documentation the user reads before they type. It
    // used to teach the shape that fails.
    await expect(repo).toHaveAttribute("placeholder", /^https:\/\//);
  });
});
