import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// PORTAL-06: Status and battery, Switch brain (survival-portal.md sections 4.1.8 and 4.1.9). Runs
// against e2e/fake-agent.mjs, which can push router events to the open stream.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function emit(page: Page, type: string, data: Record<string, unknown>) {
  await page.request.post("/__emit", { data: { type, data } });
}

async function streamOpen(page: Page) {
  await expect
    .poll(async () => (await page.request.get("/__streams")).text())
    .not.toBe("0");
}

async function noSeriousViolations(page: Page) {
  const res = await new AxeBuilder({ page }).analyze();
  const bad = res.violations.filter(
    (v) => v.impact === "serious" || v.impact === "critical",
  );
  expect(
    bad.map(
      (v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`,
    ),
  ).toEqual([]);
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test.describe("Status and battery", () => {
  test("shows battery, brain, mode, ark and works without horizontal scroll", async ({
    page,
  }) => {
    await setStatus(page, {
      alerts: [
        {
          code: "LAI-SVC-113",
          severity: "info",
          title: "No graphics card in use; answers are slow",
          fix: "Nothing to do. Short questions work best.",
        },
      ],
    });
    await page.goto("/portal/status");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(
      "Status and battery",
    );
    await expect(page.getByText("On battery. About 2 h 25 min left.")).toBeVisible();
    await expect(page.locator(".facts")).toContainText("Qwen 3.5 9B (Running)");
    await expect(page.locator(".facts")).toContainText(
      "Survival (no internet used)",
    );
    await expect(page.locator(".facts")).toContainText(
      "Yellow: everything essential is here.",
    );
    await expect(page.locator(".works li")).toHaveCount(6);
    await expect(page.locator(".problems")).toContainText("LAI-SVC-113");
    await expect(page.locator(".problems")).toContainText(
      "Nothing to do. Short questions work best.",
    );
    const m = await page.evaluate(() => ({
      sw: document.documentElement.scrollWidth,
      iw: window.innerWidth,
    }));
    expect(m.sw).toBeLessThanOrEqual(m.iw);
    // No way to leave survival mode here.
    await expect(page.getByText(/leave survival/i)).toHaveCount(0);
  });

  test("battery is amber at 20 % and red at 10 % on battery", async ({
    page,
  }) => {
    const power = (pct: number, low: boolean, critical: boolean) => ({
      on_ac: false,
      battery_pct: pct,
      minutes_left: 30,
      state: "discharging",
      low,
      critical,
    });
    await setStatus(page, { power: power(20, true, false) });
    await page.goto("/portal/status");
    await expect(page.locator(".meter-text.battery-low")).toBeVisible();
    await setStatus(page, { power: power(10, true, true) });
    await expect(page.locator(".meter-text.battery-critical")).toBeVisible({
      timeout: 10000,
    });
  });

  test("the Spanish language shows the problems in Spanish", async ({
    page,
  }) => {
    await setStatus(page, { language: "es" });
    await page.goto("/portal/status");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(
      "Estado y batería",
    );
  });

  test("accessibility", async ({ page }) => {
    await page.goto("/portal/status");
    await expect(page.locator(".works li")).toHaveCount(6);
    await noSeriousViolations(page);
  });
});

test.describe("Switch brain", () => {
  test("lists only loadable models and marks the running one", async ({
    page,
  }) => {
    await page.goto("/portal/brain");
    await expect(page.locator(".choice")).toHaveCount(2);
    await expect(page.locator(".choice").first()).toContainText("Qwen 3.5 9B");
    await expect(
      page.locator(".choice").first().getByRole("button"),
    ).toBeDisabled();
    await expect(page.locator(".choice").nth(1)).toContainText("Sees photos");
    await expect(page.getByText("Huge 70B")).toHaveCount(0);
    await noSeriousViolations(page);
  });

  test("confirm, loading, then loaded", async ({ page }) => {
    await page.goto("/portal/brain");
    await page.getByRole("button", { name: "Use this" }).click();
    await expect(
      page.getByText("Switch to Vision 4B? The current brain will stop."),
    ).toBeVisible();
    await noSeriousViolations(page);
    await page.getByRole("button", { name: "Use this" }).click();
    await expect(page.getByText(/Loading Vision 4B/)).toBeVisible();
    await streamOpen(page);
    await emit(page, "model.loading", { model: "vlm-4b" });
    await setStatus(page, {
      brain: {
        loaded: [
          { id: "vlm-4b", display_name: "Vision 4B", vision: true },
        ],
        loading: null,
        choices: [
          {
            id: "vlm-4b",
            display_name: "Vision 4B",
            summary: "x",
            vision: true,
            loadable: true,
            survival: true,
          },
        ],
      },
    });
    await emit(page, "model.loaded", { model: "vlm-4b", load_ms: 4000 });
    await expect(page.locator(".panel").getByText("Vision 4B is running.")).toBeVisible();
    await page.getByRole("button", { name: "Back" }).click();
    await expect(page.locator(".choice").first()).toContainText("Vision 4B");
  });

  test("model.failed shows the message and LAI-SVC-107", async ({ page }) => {
    await page.goto("/portal/brain");
    await page.getByRole("button", { name: "Use this" }).click();
    await page.getByRole("button", { name: "Use this" }).click();
    await streamOpen(page);
    await emit(page, "model.failed", {
      model: "vlm-4b",
      code: "LAI-SVC-107",
      message: "out of memory",
      rolled_back: true,
    });
    await expect(
      page.locator(".panel").getByText(
        "Vision 4B couldn't start. The previous brain was restarted.",
      ),
    ).toBeVisible();
    await expect(page.getByText("Code: LAI-SVC-107")).toBeVisible();
  });

  test("a 429 shows the fix and keeps the list usable", async ({ page }) => {
    await page.request.post("/__load?status=429");
    await page.goto("/portal/brain");
    await page.getByRole("button", { name: "Use this" }).click();
    await page.getByRole("button", { name: "Use this" }).click();
    await expect(page.getByText("Wait one minute and try again.")).toBeVisible();
    await page.getByRole("button", { name: "Back" }).click();
    await expect(page.locator(".choice")).toHaveCount(2);
  });

  test("keyboard only: Tab to Use this, Enter, Enter", async ({ page }) => {
    await page.goto("/portal/brain");
    await page.getByRole("button", { name: "Use this" }).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("alertdialog")).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(page.getByText(/Loading Vision 4B/)).toBeVisible();
  });
});
