import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// #430: the fix of a problem is a button (doctor-codes.md section 1.7, survival-portal.md sections
// 4.2 and 4.1.8): the Unavailable screen and the Status screen's problem list show it, it says what
// happened in plain words, the typed command is the second line, and an error keeps its code.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function seen(page: Page): Promise<{ path: string; body?: string }[]> {
  return (await page.request.get("/__seen")).json();
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

const emptyLibrary = {
  capabilities: {
    chat: { state: "ready" },
    library: { state: "unavailable", code: "LAI-SVC-001", reason: "no_books", action: "ark.import", books: [] },
  },
  alerts: [
    { code: "LAI-SVC-001", severity: "error", title: "The library has no books", fix: "Plug in the ark drive, or run `aictl ark import`.", action: "ark.import" },
  ],
};

test("an empty library offers Copy from the AI-ARK drive, with the command as a second line", async ({ page }) => {
  await setStatus(page, emptyLibrary);
  await page.goto("/portal/library");
  const button = page.getByRole("button", { name: "Copy from the AI-ARK drive" });
  await expect(button).toBeVisible();
  await expect(page.getByText("Or type in a terminal: aictl ark import")).toBeVisible();
  await button.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator(".fix-result", { hasText: "Copying from the drive has started." })).toBeVisible();
  const posts = (await seen(page)).filter((s) => s.path === "/api/v1/ark/import");
  expect(posts).toHaveLength(1);
  expect(posts[0].body).toBe("{}");
  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
});

test("no drive and too many attempts are shown with their code", async ({ page }) => {
  await setStatus(page, emptyLibrary);
  await page.request.get("/__fix?status=409");
  await page.goto("/portal/library");
  await page.getByRole("button", { name: "Copy from the AI-ARK drive" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Code: LAI-SVC-118" })).toContainText("Plug in the AI-ARK drive");
  await page.request.get("/__fix?status=429");
  await page.getByRole("button", { name: "Copy from the AI-ARK drive" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Code: LAI-SVC-117" })).toContainText("That did not work");
});

test("the Status screen's problem list has the button of each fix", async ({ page }) => {
  await setStatus(page, {
    alerts: [
      { code: "LAI-SVC-002", severity: "error", title: "The library service isn't running", fix: "Press **Restart library**, or run `aictl services restart kiwix`.", action: "service.restart:kiwix" },
      { code: "LAI-SVC-106", severity: "warning", title: "No brain is running", fix: "Press **Switch brain** and choose one.", action: "portal.brain" },
      { code: "LAI-SVC-013", severity: "info", title: "Speech recognition uses the processor", fix: "Nothing to do." },
    ],
  });
  await page.goto("/portal/status");
  await expect(page.locator(".problems .fix")).toHaveCount(2);
  await expect(page.getByText("Or type in a terminal: aictl services restart kiwix")).toBeVisible();
  await page.getByRole("button", { name: "Restart the library" }).click();
  await expect(page.locator(".fix-result", { hasText: "Done: it was restarted." })).toBeVisible();
  expect((await seen(page)).some((s) => s.path === "/api/v1/services/kiwix/restart")).toBe(true);
  await page.getByRole("button", { name: "Choose a brain" }).click();
  await expect(page).toHaveURL(/\/portal\/brain$/);
});

test("the buttons are in Spanish too", async ({ page }) => {
  await setStatus(page, { ...emptyLibrary, language: "es" });
  await page.goto("/portal/library");
  await page.getByRole("button", { name: "Español" }).click();
  await expect(page.getByRole("button", { name: "Copiar desde la unidad AI-ARK" })).toBeVisible();
  await expect(page.getByText("O escriba en una terminal: aictl ark import")).toBeVisible();
});
