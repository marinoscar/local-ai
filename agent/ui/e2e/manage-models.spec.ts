import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// Issue #498 (dashboard.md sections 3.2, 4.4, 5): the AI models screen.

type Seen = { path: string; method?: string; body?: string; query?: string; grant?: string | null };

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function seen(page: Page): Promise<Seen[]> {
  return (await (await page.request.get("/__seen")).json()) as Seen[];
}

const row = (page: Page, name: string) => page.locator("li.model", { hasText: name });

async function open(page: Page, mode = "online") {
  await setStatus(page, { mode, session: "full" });
  await page.goto("/portal/manage/models");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("AI models");
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test("lists each model with its state, size, tags and fit, state first", async ({ page }) => {
  await open(page);
  await expect(page.locator(".model-summary")).toContainText("In use now: Qwen 3.5 9B");
  await expect(page.locator(".model-summary")).toContainText("Starts with the computer: Qwen 3.5 9B");
  const qwen = row(page, "Qwen 3.5 9B");
  await expect(qwen).toContainText("In use");
  await expect(qwen).toContainText("Size: 5.9 GB");
  await expect(qwen.locator(".tag")).toHaveText(["Default", "Survival brain"]);
  await expect(qwen.getByRole("button", { name: "Unload" })).toBeEnabled();
  await expect(qwen.getByRole("button", { name: "Delete" })).toBeDisabled();
  await expect(qwen).toContainText("It is in use. Press Unload first.");
  await expect(row(page, "Vision 4B")).toContainText("On this computer");
  await expect(row(page, "Big 27B")).toContainText("Not downloaded");
  const huge = row(page, "Huge 70B");
  await expect(huge).toContainText("Too big for this computer: Needs 44.0 GB");
  await expect(huge.getByRole("button", { name: "Download" })).toBeDisabled();
  await expect(page.locator(".model-more summary")).toHaveText("More models (not planned for this computer): 1");
  await expect(row(page, "Whisper small")).toContainText("Speech");
  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
});

test("Use this brain asks, sends the load and waits until the brain is in use", async ({ page }) => {
  await open(page);
  await row(page, "Vision 4B").getByRole("button", { name: "Use this brain" }).click();
  const dlg = page.getByRole("dialog");
  await expect(dlg).toContainText("Use Vision 4B?");
  await dlg.getByRole("button", { name: "Use this brain" }).click();
  await expect(row(page, "Vision 4B")).toContainText("Vision 4B is in use.");
  await expect(row(page, "Vision 4B").locator(".model-state")).toContainText("In use");
  const load = (await seen(page)).find((s) => s.path === "/api/v1/models/vlm-4b/load");
  expect(JSON.parse(load?.body ?? "{}")).toEqual({ wait: false, unload_others: true });
});

test("Make default sends PUT /models/default", async ({ page }) => {
  await open(page);
  await row(page, "Vision 4B").getByRole("button", { name: "Make default" }).click();
  await expect(row(page, "Vision 4B")).toContainText("Vision 4B will start each time the computer starts.");
  await expect(row(page, "Vision 4B").locator(".tag").first()).toHaveText("Default");
  const put = (await seen(page)).find((s) => s.path === "/api/v1/models/default");
  expect(put?.method).toBe("PUT");
  expect(JSON.parse(put?.body ?? "{}")).toEqual({ id: "vlm-4b" });
});

test("Download is disabled offline with the reason, and online shows progress until done", async ({ page }) => {
  await open(page, "offline");
  const big = row(page, "Big 27B");
  await expect(big.getByRole("button", { name: "Download" })).toBeDisabled();
  await expect(big).toContainText("Needs the internet");
  await setStatus(page, { mode: "online" });
  await page.reload();
  await big.getByRole("button", { name: "Download" }).click();
  await expect(page.getByRole("dialog")).toContainText("It needs 16.5 GB of disk space and the internet.");
  await page.getByRole("dialog").getByRole("button", { name: "Download" }).click();
  await expect(big.locator(".model-state")).toContainText("Downloading");
  await expect(big).toContainText("Big 27B is downloaded and checked.", { timeout: 15_000 });
  await expect(big.locator(".model-state")).toContainText("On this computer");
  expect((await seen(page)).some((s) => s.path === "/api/v1/models/big-27b/download")).toBe(true);
});

test("Delete explains, asks the PIN (wrong then right), and the last copy asks once more", async ({ page }) => {
  await open(page);
  await row(page, "Vision 4B").getByRole("button", { name: "Delete" }).click();
  const dlg = page.getByRole("dialog");
  await expect(dlg).toContainText(
    "This removes the model from this computer. You can download it again later with the internet, or copy it from the AI-ARK drive.",
  );
  await dlg.getByRole("button", { name: "Delete" }).click();
  const pin = page.getByRole("dialog", { name: "Admin PIN needed" });
  await expect(pin).toContainText("Deleting Vision 4B needs the admin PIN.");
  await pin.getByLabel("Admin PIN").fill("1111");
  await pin.getByRole("button", { name: "Continue" }).click();
  await expect(pin).toContainText("That PIN is not right. 4 tries left.");
  expect((await seen(page)).some((s) => s.method === "DELETE")).toBe(false);
  await pin.getByLabel("Admin PIN").fill("4821");
  await pin.getByRole("button", { name: "Continue" }).click();
  const last = page.getByRole("dialog", { name: "This is the last copy" });
  await expect(last).toBeVisible();
  await last.getByRole("button", { name: "Delete the last copy" }).click();
  await expect(row(page, "Vision 4B")).toContainText("Vision 4B was removed from this computer.");
  await expect(row(page, "Vision 4B").locator(".model-state")).toContainText("Not downloaded");
  const dels = (await seen(page)).filter((s) => s.method === "DELETE");
  expect(dels.map((d) => [d.query, d.grant])).toEqual([
    ["?root=internal&keep_last=true", "pg_fake"],
    ["?root=internal&keep_last=false", "pg_fake"],
  ]);
});

test("an error shows its code and fix, and the screen stays usable", async ({ page }) => {
  await open(page, "survival");
  await page.request.post("/__models?fail=unload");
  await row(page, "Qwen 3.5 9B").getByRole("button", { name: "Unload" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Unload" }).click();
  const fail = row(page, "Qwen 3.5 9B").locator(".fix-failed");
  await expect(fail).toContainText("Code: LAI-AGT-023");
  await expect(fail).toContainText("Start another brain first, then unload this one.");
  await expect(row(page, "Vision 4B").getByRole("button", { name: "Use this brain" })).toBeEnabled();
});

test("the screen reads in Spanish", async ({ page }) => {
  await setStatus(page, { mode: "online", session: "full" });
  await page.addInitScript(() => window.localStorage.setItem("lai.portal.lang", "es"));
  await page.goto("/portal/manage/models");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Modelos de IA");
  await expect(row(page, "Vision 4B").getByRole("button", { name: "Usar este cerebro" })).toBeVisible();
  await expect(row(page, "Qwen 3.5 9B")).toContainText("Tamaño: 5,9 GB");
});
