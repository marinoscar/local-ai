import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// Issue #494 (dashboard.md sections 3.2, 4.2; ADR-0011): switch modes and leave survival from the
// window, with no PIN for the app's operator session.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}
async function seen(page: Page) {
  return (await (await page.request.get("/__seen")).json()) as {
    path: string;
    method?: string;
    body?: unknown;
    grant?: string | null;
  }[];
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test("Mode shows the three choices with the current one marked", async ({ page }) => {
  await setStatus(page, { mode: "online", session: "full" });
  await page.goto("/portal/manage/mode");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Mode");
  await expect(page.locator(".mode-card")).toHaveCount(3);
  await expect(page.locator(".mode-on")).toContainText("Online");
  await expect(page.locator(".mode-on")).toContainText("In use now");
  await expect(page.getByRole("button", { name: "Use Online" })).toHaveCount(0);
  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
});

test("going offline asks first, says what changes, and sends POST /mode", async ({ page }) => {
  await setStatus(page, { mode: "online", session: "full" });
  await page.goto("/portal/manage/mode");
  await page.getByRole("button", { name: "Use Offline" }).click();
  const dialog = page.getByRole("dialog", { name: "Change to Offline?" });
  await expect(dialog).toContainText("Chat, library, maps and voice keep working.");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  expect((await seen(page)).filter((s) => s.path === "/api/v1/mode")).toEqual([]);
  await page.getByRole("button", { name: "Use Offline" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Use Offline" }).click();
  await expect(page.locator(".mode-on")).toContainText("Offline");
  await expect(page.getByRole("heading", { level: 1, name: "Mode" })).toBeVisible();
  const posts = (await seen(page)).filter((s) => s.path === "/api/v1/mode");
  expect(posts).toEqual([{ path: "/api/v1/mode", method: "POST", body: { target: "offline" }, grant: null }]);
});

test("a change that takes time says so and the choices wait", async ({ page }) => {
  await setStatus(page, { mode: "online", session: "full" });
  await page.request.post("/__mode?slow=1");
  await page.goto("/portal/manage/mode");
  await page.getByRole("button", { name: "Use Offline" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Use Offline" }).click();
  await expect(page.locator(".card[role=status]").first()).toContainText("Changing to Offline");
  await expect(page.getByRole("button", { name: "Use Survival" })).toBeDisabled();
  await page.request.post("/__mode?finish=1");
  await expect(page.locator(".mode-on")).toContainText("Offline", { timeout: 15000 });
});

test("Start survival from the Mode screen uses POST /mode/survival", async ({ page }) => {
  await setStatus(page, { mode: "offline", session: "full" });
  await page.goto("/portal/manage/mode");
  await page.getByRole("button", { name: "Use Survival" }).click();
  await expect(page.getByRole("dialog")).toContainText("The survival brain is loaded.");
  await page.getByRole("dialog").getByRole("button", { name: "Use Survival" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Mode");
  await expect.poll(async () => (await seen(page)).some((s) => s.path === "/api/v1/mode/survival")).toBe(true);
});

test("Leave survival mode from Home: no PIN, choose Online, sends one POST /mode", async ({ page }) => {
  await setStatus(page, { mode: "survival", session: "full" });
  await page.goto("/portal");
  await page.getByRole("button", { name: "Leave survival mode" }).click();
  const dialog = page.getByRole("dialog", { name: "Leave survival mode?" });
  await expect(dialog).toContainText("Nothing is deleted.");
  await expect(dialog.getByRole("radio", { name: /Online/ })).toBeChecked();
  await expect(page.getByRole("dialog", { name: "Admin PIN needed" })).toHaveCount(0);
  await dialog.getByRole("button", { name: "Leave survival mode" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Local AI · Online");
  const posts = (await seen(page)).filter((s) => s.path === "/api/v1/mode" || s.path === "/api/v1/pin/verify");
  expect(posts).toEqual([{ path: "/api/v1/mode", method: "POST", body: { target: "online" }, grant: null }]);
});

test("without internet, Leave survival preselects Offline", async ({ page }) => {
  await setStatus(page, { mode: "survival", session: "full" });
  await page.request.post("/__mode?connectivity=none");
  await page.goto("/portal/manage/mode");
  await page.getByRole("button", { name: "Use Offline" }).click();
  const dialog = page.getByRole("dialog", { name: "Leave survival mode?" });
  await expect(dialog.getByRole("radio", { name: /Offline/ })).toBeChecked();
  await dialog.getByRole("button", { name: "Leave survival mode" }).click();
  await expect(page.locator(".mode-on")).toContainText("Offline");
});

test("a Portal session has no Leave button", async ({ page }) => {
  await setStatus(page, { mode: "survival", session: "portal" });
  await page.goto("/portal");
  await expect(page.getByRole("button", { name: "Leave survival mode" })).toHaveCount(0);
});

test("a refusal shows its code and the fix", async ({ page }) => {
  await setStatus(page, { mode: "online", session: "full" });
  await page.request.post("/__mode?busy=1");
  await page.goto("/portal/manage/mode");
  await page.getByRole("button", { name: "Use Offline" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Use Offline" }).click();
  await expect(page.locator(".fix-failed")).toContainText("LAI-AGT-022");
  await expect(page.locator(".fix-failed")).toContainText("Wait for it to finish");
});

test("the screen is in Spanish too", async ({ page }) => {
  await setStatus(page, { mode: "survival", session: "full" });
  await page.goto("/portal");
  await page.getByRole("button", { name: "Español" }).click();
  await page.getByRole("button", { name: "Salir del modo supervivencia" }).click();
  await expect(page.getByRole("dialog", { name: "¿Salir del modo supervivencia?" })).toBeVisible();
});
