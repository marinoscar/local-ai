import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// Issue #499 (dashboard.md sections 3.2 and 4.5): the Health screen. The fake routes are
// e2e/fake-manage/health.mjs.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function setHealth(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__health", { data: patch });
}

async function seen(page: Page): Promise<{ path: string; method?: string; query?: string; body?: string }[]> {
  return (await page.request.get("/__seen")).json();
}

async function noSeriousAxe(page: Page) {
  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
  await setStatus(page, { mode: "online", session: "full" });
});

test("opening Health runs the full check: passes, a warning with its fix button, a note", async ({ page }) => {
  await setHealth(page, { doctorDelayMs: 800 });
  await page.goto("/portal/manage/health");
  await expect(page.locator("#main").getByText("Checking this computer … this takes up to a minute.")).toBeVisible();
  const card = page.getByRole("region", { name: "Full check" });
  await expect(card.getByText("Things to fix: 1")).toBeVisible();
  await expect(card.getByText("5 checks passed.")).toBeVisible();
  const problem = card.locator(".problem-warning");
  await expect(problem).toContainText("LAI-SVC-002");
  await expect(problem).toContainText("The library is not answering");
  await expect(problem).toContainText("Restart the library");
  await expect(card.getByText("Good to know")).toBeVisible();
  await expect(card.locator(".problem-info")).toContainText("LAI-AGT-015");
  await noSeriousAxe(page);

  await problem.getByRole("button", { name: "Restart the library" }).click();
  await expect(problem.locator(".fix-result")).toBeVisible();
  expect((await seen(page)).some((s) => s.path === "/api/v1/services/kiwix/restart")).toBe(true);
  expect((await seen(page)).filter((s) => s.path === "/api/v1/doctor")).toHaveLength(1);
});

test("an all-OK check says so in green words", async ({ page }) => {
  await setHealth(page, { doctor: "ok" });
  await page.goto("/portal/manage/health");
  const card = page.getByRole("region", { name: "Full check" });
  await expect(card.getByText("✓ Everything is OK.")).toBeVisible();
  await expect(card.locator(".problem")).toHaveCount(0);
  await card.getByRole("button", { name: "Check again" }).click();
  await expect(card.getByText("✓ Everything is OK.")).toBeVisible();
  await expect.poll(async () => (await seen(page)).filter((s) => s.path === "/api/v1/doctor").length).toBe(2);
});

test("a check that fails shows its code and fix, and the screen stays usable", async ({ page }) => {
  await setHealth(page, { doctor: "error" });
  await page.goto("/portal/manage/health");
  const card = page.getByRole("region", { name: "Full check" });
  const note = card.locator(".fix-failed");
  await expect(note).toContainText("The agent is stopping");
  await expect(note).toContainText("Wait a minute and try again.");
  await expect(note).toContainText("LAI-AGT-024");
  await setHealth(page, { doctor: "ok" });
  await card.getByRole("button", { name: "Run a full check" }).click();
  await expect(card.getByText("✓ Everything is OK.")).toBeVisible();
  await expect(card.locator(".fix-failed")).toHaveCount(0);
});

test("recent events are sentences with time, level glyph and code, never JSON", async ({ page }) => {
  await page.goto("/portal/manage/health");
  const card = page.getByRole("region", { name: "Recent events" });
  await card.getByLabel("Events of").selectOption("kiwix");
  await card.getByRole("button", { name: "Show recent events" }).click();
  const items = card.locator(".health-event");
  await expect(items).toHaveCount(3);
  await expect(card.getByText("The last 3 events, newest first.")).toBeVisible();
  await expect(items.nth(0)).toContainText("kiwix-serve exited with status 1");
  await expect(items.nth(0).locator(".mark")).toHaveText("✗");
  await expect(items.nth(1)).toContainText("the library is not answering");
  await expect(items.nth(1)).toContainText("Code: LAI-SVC-002");
  await expect(items.nth(1).locator(".mark")).toHaveText("!");
  await expect(items.nth(2)).toContainText("the agent started");
  await expect(card.locator(".health-events")).not.toContainText("{");
  await expect(card.locator(".health-events")).not.toContainText('"msg"');
  const q = (await seen(page)).find((s) => s.path === "/api/v1/logs");
  expect(q?.query).toBe("?unit=kiwix&lines=50");
  await noSeriousAxe(page);
});

test("events that cannot be read show the code; an empty journal says so", async ({ page }) => {
  await setHealth(page, { logs: "error" });
  await page.goto("/portal/manage/health");
  const card = page.getByRole("region", { name: "Recent events" });
  await card.getByRole("button", { name: "Show recent events" }).click();
  await expect(card.locator(".fix-failed")).toContainText("LAI-AGT-099");
  await setHealth(page, { logs: "none" });
  await card.getByRole("button", { name: "Show recent events" }).click();
  await expect(card.getByText("Nothing was written lately.")).toBeVisible();
});

test("a practice drill: confirm, progress, then the result with codes", async ({ page }) => {
  await setHealth(page, { drill: "last", drillPolls: 2 });
  await page.goto("/portal/manage/health");
  const card = page.getByRole("region", { name: "Practice drill" });
  await expect(card.getByText("✓ Passed. Everything needed in an emergency works.")).toBeVisible();
  await expect(card.getByText("Ready for survival mode: yes.")).toBeVisible();

  await card.getByRole("button", { name: "Run a practice drill" }).click();
  const dialog = page.getByRole("dialog", { name: "Run a practice drill?" });
  await expect(dialog).toContainText("about 2 minutes");
  await expect(dialog).toContainText("everything is put back as it was");
  await noSeriousAxe(page);
  await dialog.getByRole("button", { name: "Start the drill" }).click();

  await expect(card.getByText(/The drill is running: \d checks done so far\./)).toBeVisible();
  await expect(card.getByRole("button", { name: "Stop the drill" })).toBeVisible();
  await expect(card.getByText("✗ Failed. Something needed in an emergency does not work.")).toBeVisible({ timeout: 15000 });
  await expect(card.getByText("3 passed, 1 with warnings, 1 failed, 0 skipped.")).toBeVisible();
  await expect(card.getByText("Ready for survival mode: no.")).toBeVisible();
  const attention = card.locator(".problem");
  await expect(attention.nth(0)).toContainText("Library search");
  await expect(attention.nth(0)).toContainText("LAI-DRL-030");
  await expect(attention.nth(1)).toContainText("Speech to text (Spanish)");
  await expect(attention.nth(1)).toContainText("LAI-DRL-031");
  const post = (await seen(page)).find((s) => s.path === "/api/v1/drill" && s.method === "POST");
  expect(JSON.parse(post?.body ?? "{}")).toEqual({ quick: true });
});

test("Stop ends a running drill and says everything was put back", async ({ page }) => {
  await setHealth(page, { drillPolls: 1000 });
  await page.goto("/portal/manage/health");
  const card = page.getByRole("region", { name: "Practice drill" });
  await expect(card.getByText("No practice drill has run on this computer yet.")).toBeVisible();
  await card.getByRole("button", { name: "Run a practice drill" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Start the drill" }).click();
  await card.getByRole("button", { name: "Stop the drill" }).click();
  await expect(card.getByText("The drill was stopped before it ended. Everything was put back as it was.")).toBeVisible({
    timeout: 15000,
  });
  await expect(card.getByText("Why: the drill was stopped")).toBeVisible();
  expect((await seen(page)).some((s) => s.path === "/api/v1/drill" && s.method === "DELETE")).toBe(true);
  await expect(card.getByRole("button", { name: "Run a practice drill" })).toBeEnabled();
});

test("a refused drill shows its code and fix; survival mode disables the button with the reason", async ({ page }) => {
  await setHealth(page, { drillStart: 409 });
  await page.goto("/portal/manage/health");
  const card = page.getByRole("region", { name: "Practice drill" });
  await card.getByRole("button", { name: "Run a practice drill" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Start the drill" }).click();
  const note = card.locator(".fix-failed");
  await expect(note).toContainText("The battery is too low for a drill");
  await expect(note).toContainText("Plug in the charger, then start the drill again.");
  await expect(note).toContainText("LAI-DRL-005");

  await setStatus(page, { mode: "survival", session: "full" });
  await page.goto("/portal/manage/health");
  await expect(card.getByRole("button", { name: "Run a practice drill" })).toBeDisabled();
  await expect(card.getByText("Not available in survival mode: a drill never disturbs an emergency.")).toBeVisible();
});

test("the screen reads in Spanish", async ({ page }) => {
  await setHealth(page, { doctor: "ok" });
  await page.addInitScript(() => localStorage.setItem("lai.portal.lang", "es"));
  await page.goto("/portal/manage/health");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Salud");
  await expect(page.locator("#main").getByText("✓ Todo está bien.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Mostrar sucesos recientes" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Hacer un simulacro" })).toBeVisible();
});
