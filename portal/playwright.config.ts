import { defineConfig, devices } from "@playwright/test";

// Gate B #3 Slice B — portal RBAC-scope journeys. These run against a REAL stack
// (Go API + seeded Postgres + the built portal), so authorization is proven the
// way a user experiences it, not mocked. The API and portal are started by the
// CI job (and by scripts/e2e-stack for local runs) before this runs; baseURL is
// the portal. Server-side role×route RBAC is already exhaustively unit-tested in
// Go (portal_test.go) — this proves the browser story: per-role nav, out-of-role
// route refusal, and the scope indicator.
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "line" : "list",
  use: {
    baseURL: process.env.PORTAL_BASE_URL || "http://localhost:3090",
    trace: "on-first-retry",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
