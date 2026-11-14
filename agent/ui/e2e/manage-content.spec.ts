import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// Issue #496 (dashboard.md sections 3.2, 4.3, 5, 7): the Offline content and drives screen.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

type Seen = { path: string; method?: string; body?: string; grant?: string };
async function seen(page: Page): Promise<Seen[]> {
  return (await (await page.request.get("/__seen")).json()) as Seen[];
}

async function open(page: Page, mode = "online") {
  await setStatus(page, { mode, session: "full" });
  await page.goto("/portal/manage/content");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Offline content and drives");
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test("online: readiness first, items grouped with their state in plain words", async ({ page }) => {
  await open(page);
  await expect(page.locator(".lead")).toHaveText("Some content is missing. Next step: press Sync now.");
  await expect(page.locator(".content-root")).toContainText("Internal disk: 60 % ready, 2 items missing");
  await expect(page.locator(".content-root")).toContainText("Last sync:");
  const items = page.locator(".content-rootitems");
  await expect(items.locator(".content-group-title")).toHaveText(["Maps", "Library", "AI models", "Speech"]);
  await expect(items.locator(".content-item", { hasText: "osm-north-america" })).toContainText("Ready");
  await expect(items.locator(".content-item", { hasText: "wikipedia-en-maxi" })).toContainText("Not downloaded yet");
  const blocked = items.locator(".content-item", { hasText: "wikem-en" });
  await expect(blocked).toContainText("Skipped until a new version is published");
  await expect(blocked).toContainText("Code: LAI-SYN-020");
  await expect(items.locator(".content-item", { hasText: "gutenberg-en" })).toContainText("Not in the plan");
  await expect(page.locator(".content-last")).toContainText("1 item did not work");

  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
});

test("Sync now shows the progress, and Stop stops it", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: "Sync now" }).click();
  await expect(page.locator(".content-progress")).toContainText("Downloading the newest content");
  await expect(page.locator(".content-progress")).toContainText("2 of 6 items, 25 %");
  await expect(page.locator(".content-active")).toContainText("wikipedia-en-maxi · downloading");
  await expect(page.getByRole("button", { name: "Check content" })).toBeDisabled();
  await expect(page.getByText("Check content: Wait until the current work finishes, or press Stop.")).toBeVisible();
  await page.getByRole("button", { name: "Stop" }).click();
  await expect(page.locator(".content-last")).toContainText("The last run was stopped.");
  const s = await seen(page);
  expect(s.find((x) => x.path === "/api/v1/ark/sync" && x.method === "POST")?.body).toBe('{"trigger":"manual"}');
  expect(s.some((x) => x.path === "/api/v1/ark/sync" && x.method === "DELETE")).toBe(true);
});

test("offline: Sync now is disabled and says it needs the internet", async ({ page }) => {
  await open(page, "offline");
  await expect(page.getByRole("button", { name: "Sync now" })).toBeDisabled();
  await expect(page.getByText("Sync now: Needs the internet")).toBeVisible();
  await expect(page.locator(".lead")).toHaveText("Some content is missing. Next step: press Copy from this drive.");
  await expect(page.getByRole("button", { name: "Check content" })).toBeEnabled();
});

test("Copy from this drive starts an import from that drive", async ({ page }) => {
  await open(page, "offline");
  await page.getByRole("button", { name: "Copy from this drive" }).click();
  await expect(page.locator(".content-progress")).toContainText("Copying from the AI-ARK drive");
  const imp = (await seen(page)).find((x) => x.path === "/api/v1/ark/import");
  expect(JSON.parse(imp?.body ?? "{}")).toEqual({ from_root: "ark-3f9c0a12" });
});

test("Eject safely says when it is safe to unplug", async ({ page }) => {
  await open(page);
  const drive = page.locator(".content-drive").first();
  await drive.getByRole("button", { name: "Eject safely" }).click();
  await expect(page.getByText("AI-ARK, 2 TB: you can unplug the drive now.")).toBeVisible({ timeout: 10_000 });
  await expect(page.locator(".content-drive")).toHaveCount(1);
});

test("Prepare a new drive confirms, asks the PIN and sends the grant", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: "Prepare a new drive" }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm).toContainText("Nothing already on the drive is deleted.");
  await confirm.getByRole("button", { name: "Yes, prepare it" }).click();
  const pin = page.getByRole("dialog");
  await expect(pin).toContainText("Admin PIN needed");
  await pin.getByLabel("Admin PIN", { exact: true }).fill("4821");
  await pin.getByRole("button", { name: "Continue" }).click();
  await expect(page.locator(".fix-result", { hasText: "The drive is ready for Local AI. Press Sync now to fill it." })).toBeVisible();
  const init = (await seen(page)).find((x) => x.path === "/api/v1/ark/drives/ark-77aa0011/init");
  expect(init?.grant).toBe("pg_fake");
  expect(init?.body).toBe("{}");
});

test("Remove content not in the plan asks the PIN, lists what is lost, then removes it", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: "Remove content not in the plan" }).click();
  await page.getByLabel("Admin PIN", { exact: true }).fill("4821");
  await page.getByRole("button", { name: "Continue" }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm).toContainText("This deletes 1 item (7 GB) listed below.");
  await expect(confirm).toContainText("gutenberg-en · not in the plan · 7 GB");
  await confirm.getByRole("button", { name: "Yes, remove it" }).click();
  await expect(page.locator(".fix-result", { hasText: "Done. Space freed: 7 GB." })).toBeVisible();
  const prunes = (await seen(page)).filter((x) => x.path === "/api/v1/ark/prune");
  expect(prunes.map((x) => JSON.parse(x.body ?? "{}").dry_run)).toEqual([true, false]);
  expect(prunes.every((x) => x.grant === "pg_fake")).toBe(true);
});

test("an error shows its code and its fix", async ({ page }) => {
  await page.request.post("/__content?verify=409");
  await open(page);
  await page.getByRole("button", { name: "Check content" }).click();
  const note = page.locator(".fix-failed");
  await expect(note).toContainText("That did not work: Another sync or check is already running");
  await expect(note).toContainText("Wait for it to finish, or press Stop.");
  await expect(note).toContainText("Code: LAI-SYN-005");
});

test("Spanish: the screen reads in Spanish", async ({ page }) => {
  await open(page, "offline");
  await page.getByRole("button", { name: "Español", exact: true }).first().click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Contenido sin conexión y discos");
  await expect(page.getByText("Sincronizar ahora: Necesita internet")).toBeVisible();
});
