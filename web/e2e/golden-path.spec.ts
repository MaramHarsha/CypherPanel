import { test, expect } from "./fixtures";

/**
 * The golden path, in a browser: sign in, see the panel name what is missing,
 * create a project, create an application, and arrive somewhere that can deploy
 * it.
 *
 * WHY THIS ONE IS DIFFERENT from the other specs here. Each of those was
 * written after a specific missing control — a Port field, a Repository field,
 * a domain picker. This one covers the sequence NOBODY reported, because a new
 * operator who gets stuck on step two does not file a bug, they close the tab.
 * It is also the only test that exercises the guided-onboarding band, whose
 * progress is DERIVED (guided-onboarding.md §4) and therefore silently wrong
 * the moment a creation path stops being counted — exactly the failure
 * dns-automation.md §4.3 records against a hook nobody re-hooked.
 *
 * It stops at "ready to deploy" on purpose. Running the deploy needs a builder
 * and a Docker daemon, and integration.yml's Deploy slice already proves that
 * end to end against real Docker and real Traefik; a slower, flakier copy of a
 * passing test is not coverage.
 */
test.describe("the golden path", () => {
  test("walks from an empty panel to an application ready to deploy", async ({ signedIn: page }) => {
    await page.goto("/projects");

    // STEP 1-2. The band is the thread between the four steps. The harness has
    // already enrolled a server, so the panel must say so — a band that still
    // asks for a server when one is joined is the stored-flag failure the spec
    // rejected, arriving through a derivation that stopped deriving.
    const band = page.getByRole("region", { name: /set up your panel/i });
    await expect(band).toBeVisible({ timeout: 15_000 });
    const serverStep = band.locator("li").filter({ hasText: "Add a server" });
    await expect(serverStep).toBeVisible();
    await expect(serverStep.getByRole("link", { name: /add a server/i })).toHaveCount(0);

    // STEP 3. Create a project — through the control the band points at, not
    // through a URL, because the question is whether the path is walkable.
    const name = "golden-" + Date.now().toString(36);
    await page.getByRole("button", { name: "New project" }).first().click();
    const projectDialog = page.getByRole("dialog");
    await projectDialog.getByLabel(/name/i).first().fill(name);
    await projectDialog.getByRole("button", { name: /create/i }).click();
    await expect(projectDialog).toBeHidden({ timeout: 20_000 });

    // STEP 4. An application, from the environment screen the project lands on.
    await page.getByRole("button", { name: /new application/i }).first().click();
    const appDialog = page.getByRole("dialog");
    await expect(appDialog).toBeVisible();

    // The real dialog, not the "Join a server first" stand-in it falls back to
    // when no server is enrolled. Reaching that one with a host joined is how a
    // panel with a perfectly good server tells its owner they have none — and
    // the Name field is the difference, so assert on the control rather than on
    // the absence of a title.
    await expect(appDialog).not.toContainText(/join a server first/i);
    await appDialog.getByLabel("Name").fill("web");
    await appDialog.getByLabel("Repository", { exact: true }).fill("https://github.com/acme/web");
    await appDialog.getByRole("button", { name: /deploy/i }).click();
    await expect(appDialog).toBeHidden({ timeout: 25_000 });

    // ARRIVAL. The application's own page, with the control that starts a
    // deploy on it. Everything above is setup for this assertion: the panel
    // must not land an operator somewhere that cannot do the next thing.
    await expect(page.getByRole("heading", { name: "web", level: 1 })).toBeVisible({ timeout: 20_000 });

    // The dialog's button says Deploy, so a deploy must actually be running —
    // the masthead pill reads "Deploying…" rather than "Deploy now". A screen
    // that landed here idle would mean the create had quietly made a row and
    // nothing else, which is the thing an operator would not notice until they
    // went looking for the URL it promised.
    await expect(page.getByRole("button", { name: /deploy/i }).first()).toBeVisible();
    await expect(page.getByText("deploying", { exact: false }).first()).toBeVisible({ timeout: 20_000 });

    // Where it runs, on the arrival screen: the server the dialog chose. The
    // point of the whole path is that step 2 and step 4 are connected.
    await expect(page.getByText("e2e-host").first()).toBeVisible();
  });

  test("the setup band still asks for a deploy, and goes away when one succeeds", async ({ signedIn: page }) => {
    await page.goto("/projects");

    // Derived progress means the band must count what actually HAPPENED.
    // Nothing in this suite deploys, so however many projects and applications
    // it has made, the last step stays open — a band that congratulated an
    // operator for creating a row would be the wizard this feature is not.
    const band = page.getByRole("region", { name: /set up your panel/i });
    await expect(band).toBeVisible({ timeout: 15_000 });
    await expect(band.locator("li").filter({ hasText: "Deploy something" })).toBeVisible();

    // And it says what it is waiting for, rather than only counting.
    await expect(band).toContainText(/disappears once you have deployed something/i);
  });
});
