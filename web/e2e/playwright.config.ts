import { defineConfig, devices } from "@playwright/test";

/**
 * The panel is booted by the harness (scripts/e2e.sh) rather than by
 * `webServer` here: it needs a PostgreSQL container and a master key, and a
 * config file is the wrong place for that.
 */
export default defineConfig({
  testDir: ".",
  // A control that is missing does not appear late — it never appears. Short
  // timeouts keep a genuine failure fast instead of waiting 30s to say so.
  timeout: 30_000,
  expect: { timeout: 7_000 },
  // Serial: these share one panel and one database, and the fixtures create
  // real projects. Parallelism here would buy seconds and cost determinism.
  workers: 1,
  fullyParallel: false,
  // A retry hides a flake, and a flaky browser test is worse than none: it
  // teaches people to re-run until green. If one of these is unstable, that is
  // a bug in the test to fix rather than paper over.
  retries: 0,
  reporter: process.env.CI ? [["github"], ["list"]] : [["list"]],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://127.0.0.1:8099",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
