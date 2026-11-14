import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// Issue #500 (dashboard.md sections 3.2, 4.6, 5): the System screen.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function seen(page: Page): Promise<{ path: string; method?: string; body?: string }[]> {
  return (await page.request.get("/__seen")).json();
}

/** A stand-in for ai-desktop's bridge (desktop.md 3.5): records the names and answers what the test says. */
async function fakeDesktop(page: Page, answer: unknown) {
  await page.addInitScript((a) => {
    const calls: string[] = [];
    (window as unknown as Record<string, unknown>).__systemCalls = calls;
    (window as unknown as Record<string, unknown>).localaiDesktopHost = {
      isDesktop: true,
      version: "0.1.0-rc.2",
      copyApiKey: async () => ({ ok: false }),
      openWindow: () => undefined,
      connectionInfo: async () => ({ ok: false }),
      runSystemAction: async (name: string) => {
        calls.push(name);
        return a;
      },
    };
  }, answer);
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
  await setStatus(page, { mode: "online", session: "full" });
});

test("shows the versions and the services in plain words, and passes axe", async ({ page }) => {
  await page.goto("/portal/manage/system");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("System");
  const versions = page.locator(".sys-versions");
  await expect(versions).toContainText("Local AI");
  await expect(versions).toContainText("0.1.0-rc.7");
  await expect(versions).toContainText("2026.11.02-1");
  await expect(versions).not.toContainText("This app");
  const services = page.locator(".sys-service");
  await expect(services).toHaveCount(4);
  await expect(services.nth(0)).toContainText("AI brain");
  await expect(services.nth(0)).toContainText("Running");
  await expect(services.nth(1)).toContainText("Library");
  await expect(services.nth(1)).toContainText("Stopped");
  await expect(services.nth(2)).toContainText("Running, with problems");
  // Voice has no unit: no Restart.
  await expect(services.nth(3).getByRole("button")).toHaveCount(0);
  await expect(page.getByText("2 of 3 parts are ready")).toBeVisible();
  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
});

test("Restart asks first, says what stops, then sends the POST", async ({ page }) => {
  await page.goto("/portal/manage/system");
  await page.getByRole("button", { name: "Restart AI brain" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Restart AI brain?");
  await expect(dialog).toContainText("stops for up to a minute");
  expect((await seen(page)).filter((s) => s.path === "/api/v1/services/llama/restart")).toEqual([]);
  await dialog.getByRole("button", { name: "Restart" }).click();
  await expect(page.locator(".fix-done")).toHaveText("AI brain restarted.");
  expect((await seen(page)).filter((s) => s.path === "/api/v1/services/llama/restart")).toHaveLength(1);
});

test("a failed restart shows its doctor code and fix", async ({ page }) => {
  await page.request.post("/__system?restart=502");
  await page.goto("/portal/manage/system");
  await page.getByRole("button", { name: "Restart AI brain" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Restart" }).click();
  const note = page.locator(".fix-failed");
  await expect(note).toContainText("The AI brain is not running");
  await expect(note).toContainText("Code: LAI-LLM-001");
});

test("Get the newest catalog sends the refresh online, and is disabled with the reason offline", async ({ page }) => {
  await page.goto("/portal/manage/system");
  await page.getByRole("button", { name: "Get the newest catalog" }).click();
  await expect(page.locator(".fix-done")).toHaveText("The catalog was updated to 2026.11.14-1.");
  expect((await seen(page)).filter((s) => s.path === "/api/v1/catalog/refresh")).toHaveLength(1);

  await setStatus(page, { mode: "offline" });
  await page.reload();
  await expect(page.getByRole("button", { name: "Get the newest catalog" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Update Local AI" })).toBeDisabled();
  await expect(page.getByText("Needs the internet").first()).toBeVisible();
});

test("in a plain browser a privileged button shows the command for an expert", async ({ page }) => {
  await page.goto("/portal/manage/system");
  const expert = page.locator("details.expert").filter({ hasText: "aictl kit verify" });
  await expect(expert).not.toHaveAttribute("open", "");
  await page.getByRole("button", { name: "Check the rebuild kit" }).click();
  await expect(expert).toHaveAttribute("open", "");
  await expect(expert.locator("code")).toHaveText("aictl kit verify");
  await expect(page.getByText("This button works in the Local AI app.").first()).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  // The PIN change is never a button.
  await expect(page.getByRole("button", { name: "Change the admin PIN" })).toHaveCount(0);
  await expect(page.locator("details.expert code").filter({ hasText: "sudo aictl pin set" })).toHaveCount(1);
});

test("inside ai-desktop the button confirms, then calls the bridge, and says the result", async ({ page }) => {
  await fakeDesktop(page, { ok: true, overall: "green" });
  await page.goto("/portal/manage/system");
  await expect(page.locator(".sys-versions")).toContainText("0.1.0-rc.2");
  await page.getByRole("button", { name: "Check the rebuild kit" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("The computer will ask for your login password");
  await dialog.getByRole("button", { name: "Yes, do it" }).click();
  await expect(page.locator(".fix-done")).toHaveText("The rebuild kit is complete and undamaged.");
  expect(await page.evaluate(() => (window as unknown as { __systemCalls: string[] }).__systemCalls)).toEqual(["kit-verify"]);
});

test("a bridge failure shows the command's code and fix", async ({ page }) => {
  await fakeDesktop(page, { ok: false, reason: "failed", code: "LAI-ARK-001", message: "No AI-ARK drive is connected", fix: "Connect the AI-ARK drive and try again." });
  await page.goto("/portal/manage/system");
  await page.getByRole("button", { name: "Save settings to a file" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Yes, do it" }).click();
  const note = page.locator(".fix-failed");
  await expect(note).toContainText("No AI-ARK drive is connected");
  await expect(note).toContainText("Connect the AI-ARK drive and try again.");
  await expect(note).toContainText("Code: LAI-ARK-001");
});

test("Restore settings asks the admin PIN before it calls the bridge", async ({ page }) => {
  await fakeDesktop(page, { ok: true, restored: 3, restarted: true });
  await page.goto("/portal/manage/system");
  await page.getByRole("button", { name: "Restore settings" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Yes, do it" }).click();
  const pin = page.getByRole("dialog");
  await expect(pin).toContainText("Admin PIN needed");
  expect(await page.evaluate(() => (window as unknown as { __systemCalls: string[] }).__systemCalls)).toEqual([]);
  await pin.getByLabel("Admin PIN").fill("4821");
  await pin.getByRole("button", { name: "Continue" }).click();
  await expect(page.locator(".fix-done")).toHaveText("Restored 3 files. Local AI restarted.");
  expect(await page.evaluate(() => (window as unknown as { __systemCalls: string[] }).__systemCalls)).toEqual(["restore-config"]);
});

test("the screen reads in Spanish", async ({ page }) => {
  await page.addInitScript(() => window.localStorage.setItem("lai.portal.lang", "es"));
  await page.goto("/portal/manage/system");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Sistema");
  await expect(page.getByRole("button", { name: "Revisar el kit de reconstrucción" })).toBeVisible();
  await expect(page.locator(".sys-service").nth(1)).toContainText("Detenido");
});
