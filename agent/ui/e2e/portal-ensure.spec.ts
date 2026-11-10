import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// ADR-0010: the state-first card of "Install what's missing" on the Status screen
// (survival-portal.md section 4.1.8), and the doctor action `ensure.run`.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function setEnsure(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__ensure", { data: patch });
}

async function seen(page: Page): Promise<{ path: string; method?: string; lang?: string; body?: string }[]> {
  return (await page.request.get("/__seen")).json();
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test("Needs you first, then the one button; pressing it starts the install and shows progress and Stop", async ({ page }) => {
  await page.goto("/portal/status");
  const card = page.getByRole("region", { name: "What this computer needs" });
  await expect(card).toContainText("Needs you: the speech program is missing.");
  await expect(card).toContainText("Press Install what's missing.");
  const button = card.getByRole("button", { name: "Install what's missing" });
  await button.focus();
  await page.keyboard.press("Enter");
  await expect(card).toContainText("Installing what's missing (1 of 1)");
  await expect(card.getByRole("progressbar")).toBeVisible();
  await expect(card.getByRole("button", { name: "Stop installing" })).toBeVisible();
  await expect(card.getByRole("button", { name: "Install what's missing" })).toHaveCount(0);
  const posts = (await seen(page)).filter((s) => s.method === "POST");
  expect(posts).toHaveLength(1);
  expect(posts[0].body).toBe("{}");
  expect(posts[0].lang).toBe("en");
  const res = await new AxeBuilder({ page }).analyze();
  expect(res.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
});

test("Stop installing cancels and says what was kept", async ({ page }) => {
  await page.goto("/portal/status");
  const card = page.getByRole("region", { name: "What this computer needs" });
  await card.getByRole("button", { name: "Install what's missing" }).click();
  await card.getByRole("button", { name: "Stop installing" }).click();
  await expect(card.locator(".fix-result", { hasText: "Stopped. What was already installed stays." })).toBeVisible();
  expect((await seen(page)).some((s) => s.method === "DELETE")).toBe(true);
});

test("a refusal keeps its code and the button stays usable", async ({ page }) => {
  await page.request.get("/__ensure-answer?status=409");
  await page.goto("/portal/status");
  const card = page.getByRole("region", { name: "What this computer needs" });
  await card.getByRole("button", { name: "Install what's missing" }).click();
  await expect(card.getByRole("alert")).toContainText("Code: LAI-SYN-005");
  await expect(card.getByRole("button", { name: "Install what's missing" })).toBeEnabled();
  await page.request.get("/__ensure-answer?status=503");
  await card.getByRole("button", { name: "Install what's missing" }).click();
  await expect(card.getByRole("alert")).toContainText("Code: LAI-AGT-024");
});

test("Complete has no button", async ({ page }) => {
  await setEnsure(page, {
    state: "complete",
    message: "Complete: this computer has everything it needs.",
    next_action: { kind: "none", text: "" },
    steps: [],
  });
  await page.goto("/portal/status");
  const card = page.getByRole("region", { name: "What this computer needs" });
  await expect(card).toContainText("Complete: this computer has everything it needs.");
  await expect(card.getByRole("button")).toHaveCount(0);
});

test("a step that needs a person's OK says who may approve it and what it costs", async ({ page }) => {
  await setEnsure(page, {
    steps: [
      { id: "whisper.build", kind: "build_whisper", status: "needs_confirm", needs_confirmation: true, title: "Build the speech program", detail: "Compiles it from source." },
    ],
  });
  await page.goto("/portal/status");
  const card = page.getByRole("region", { name: "What this computer needs" });
  await expect(card).toContainText("It can take an hour or more");
  await expect(card).toContainText("sudo aictl ensure --yes");
  await expect(card.getByRole("button", { name: "Install what's missing" })).toHaveCount(0);
});

test("the card and its button are in Spanish too", async ({ page }) => {
  await page.goto("/portal/status");
  await page.getByRole("button", { name: "Español" }).click();
  const card = page.getByRole("region", { name: "Lo que necesita esta computadora" });
  await expect(card.getByRole("button", { name: "Instalar lo que falta" })).toBeVisible();
  await card.getByRole("button", { name: "Instalar lo que falta" }).click();
  await expect(card.getByRole("button", { name: "Detener la instalación" })).toBeVisible();
  expect((await seen(page)).filter((s) => s.method === "POST").at(-1)?.lang).toBe("es");
});

test("the doctor action ensure.run is a button on the problem list", async ({ page }) => {
  await setStatus(page, {
    alerts: [
      { code: "LAI-SVC-003", severity: "error", title: "The library program isn't installed", fix: "Press **Install what's missing**, or run `aictl ensure`.", action: "ensure.run" },
    ],
  });
  await page.goto("/portal/status");
  const fix = page.locator(".problems .fix");
  await expect(fix.getByText("Or type in a terminal: aictl ensure")).toBeVisible();
  await fix.getByRole("button", { name: "Install what's missing" }).click();
  await expect(fix.locator(".fix-result", { hasText: "Installing what's missing has started." })).toBeVisible();
});

test("it works on a small screen with the keyboard only", async ({ page }) => {
  await page.setViewportSize({ width: 640, height: 480 });
  await page.goto("/portal/status");
  const card = page.getByRole("region", { name: "What this computer needs" });
  await expect(card).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  expect(overflow).toBe(false);
  const button = card.getByRole("button", { name: "Install what's missing" });
  await button.focus();
  await expect(button).toBeFocused();
});
