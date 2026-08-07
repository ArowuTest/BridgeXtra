import { test, expect, Page } from "@playwright/test";
import { NAV } from "../lib/nav";

// Portal RBAC-scope journeys against the real Go API + seeded Postgres. Keys come
// from PORTAL_E2E_KEYS (JSON role->key), the SAME source the seed-operators tool
// used, so test and seed can never drift. A telco-scoped FINANCE key proves the
// scope indicator; every scope in the seed is '*' except that one.
type Role = "ADMIN" | "RISK" | "FINANCE" | "OPS" | "SUPPORT";

const KEYS: Record<string, string> = JSON.parse(process.env.PORTAL_E2E_KEYS || "{}");

const ROLES: Role[] = ["ADMIN", "RISK", "FINANCE", "OPS", "SUPPORT"];

// EXPECTED nav labels per role are DERIVED from lib/nav.ts NAV — the single source of
// truth the sidebar itself filters with (navFor), which in turn mirrors the server RBAC
// map. Deriving (rather than a hand-kept copy) means a new nav item can never silently
// drift this journey out of date; the test still exercises the real render pipeline
// (login -> /me role -> sidebar), so a broken filter, selector, or session shows up here.
// The server refuses an unauthorised route regardless — the nav is UX; the security oracle
// is the backend RBAC matrix (TestM4A_RBACMatrix_DenyByDefault).
const EXPECTED_NAV: Record<Role, string[]> = Object.fromEntries(
  ROLES.map((role) => [role, NAV.filter((n) => n.roles.includes(role)).map((n) => n.label)]),
) as Record<Role, string[]>;

async function login(page: Page, key: string) {
  await page.goto("/login");
  await page.locator('input[type="password"]').fill(key);
  await page.getByRole("button", { name: /sign in/i }).click();
  await page.waitForURL("**/dashboard");
  // The shell renders the sidebar once /me resolves — wait for it before asserting.
  await page.locator("aside.sidebar nav a").first().waitFor();
}

for (const role of Object.keys(EXPECTED_NAV) as Role[]) {
  test(`${role}: sidebar shows exactly its authorised surfaces`, async ({ page }) => {
    test.skip(!KEYS[role], `no seeded key for ${role}`);
    await login(page, KEYS[role]);
    const links = page.locator("aside.sidebar nav a");
    // Auto-retrying assertion: the visible nav set equals exactly this role's grants.
    await expect
      .poll(async () => (await links.allInnerTexts()).map((s) => s.trim()).filter(Boolean).sort())
      .toEqual(EXPECTED_NAV[role].slice().sort());
  });
}

// Direct navigation to an out-of-role route: the server refuses the data (RBAC is
// server-enforced, not just hidden links), so the page surfaces an error and NEVER
// renders the protected data. SUPPORT has no Ledger link — prove /finance is inert.
test("SUPPORT cannot read the ledger by direct URL (server refuses)", async ({ page }) => {
  test.skip(!KEYS.SUPPORT, "no seeded SUPPORT key");
  await login(page, KEYS.SUPPORT);
  await page.goto("/finance");
  // The error card renders on the API refusal; the journal data table never does.
  await expect(page.locator(".error")).toBeVisible();
  await expect(page.locator("table.data")).toHaveCount(0);
});

// The scope indicator: a telco-scoped operator sees a scope chip; a '*' platform
// admin does not. FINANCE_SCOPED is seeded with scope "telco:SIM_NG".
test("telco-scoped operator shows its scope; '*' admin shows none", async ({ page }) => {
  test.skip(!KEYS.FINANCE_SCOPED || !KEYS.ADMIN, "no seeded scoped/admin keys");

  await login(page, KEYS.FINANCE_SCOPED);
  await expect(page.locator("aside.sidebar .whoami")).toContainText("telco:SIM_NG");

  await login(page, KEYS.ADMIN);
  await expect(page.locator("aside.sidebar .whoami")).not.toContainText("telco:");
});
