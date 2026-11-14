import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// Issue #497 (dashboard.md sections 3.1, 4.1, 5): the Portal's Home is the Dashboard in every mode.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test('"/" opens Home in online mode too', async ({ page }) => {
  await setStatus(page, { mode: "online", session: "full" });
  await page.goto("/");
  await expect(page).toHaveURL(/\/portal$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Local AI · Online");
  await expect(page.locator(".big")).toHaveCount(8);
});

test("an operator session sees the Manage row and the status line in every mode", async ({ page }) => {
  for (const mode of ["online", "offline", "survival"]) {
    await setStatus(page, { mode, session: "full" });
    await page.goto("/portal");
    const row = page.getByRole("navigation", { name: "Manage this computer" });
    await expect(row.getByRole("button")).toHaveText(["Mode", "Offline content", "AI models", "Health", "System"]);
    await expect(page.locator(".statusline")).toContainText("Ready.");
    await expect(page.getByRole("button", { name: "Help", exact: true })).toBeVisible();
  }
  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
});

test("a Portal session sees no Manage row, and a Manage link says to use the app", async ({ page }) => {
  await page.goto("/portal");
  await expect(page.getByRole("navigation", { name: "Manage this computer" })).toHaveCount(0);
  await page.goto("/portal/manage/content");
  await expect(page.getByText("Open this in the Local AI app to change settings.")).toBeVisible();
  await page.getByRole("button", { name: "Help" }).first().click();
  await expect(page).toHaveURL(/\/portal\/help$/);
});

test("M opens Manage, H opens Help, Esc returns Home; the tabs move between screens", async ({ page }) => {
  await setStatus(page, { mode: "online", session: "full" });
  await page.goto("/portal");
  await page.keyboard.press("m");
  await expect(page).toHaveURL(/\/portal\/manage\/mode$/);
  await page.getByRole("navigation", { name: "Manage this computer" }).getByRole("button", { name: "System" }).click();
  await expect(page).toHaveURL(/\/portal\/manage\/system$/);
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/portal$/);
  await page.keyboard.press("h");
  await expect(page).toHaveURL(/\/portal\/help$/);
});

test("a status that is not green shows the one next thing to do", async ({ page }) => {
  await setStatus(page, {
    mode: "online",
    session: "full",
    capabilities: { chat: { state: "ready" }, library: { state: "ready" }, maps: { state: "degraded", code: "LAI-SVC-031" } },
  });
  await page.goto("/portal");
  await expect(page.locator(".next-step")).toContainText("Next step:");
  await page.locator(".next-step").getByRole("button", { name: "Run a full check" }).click();
  await expect(page).toHaveURL(/\/portal\/manage\/health$/);
});
