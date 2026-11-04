import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// PORTAL-11: the first-aid row on Home and the full grid (survival-portal.md section 4.1.10).
// Runs against e2e/fake-agent.mjs; link resolution and availability are covered by the Go tests.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

const entry = (key: string, title: string, available = true) => ({
  key,
  title,
  url: `/library/content/mdwiki_en_all/${title}`,
  available,
});

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test("home row: a hidden entry is not shown, an entry opens in the reader, Esc goes home", async ({
  page,
}) => {
  await setStatus(page, {
    first_aid: [
      entry("cpr", "CPR"),
      entry("choking", "Choking", false),
      entry("bleeding", "Severe bleeding"),
    ],
  });
  await page.goto("/portal");
  const row = page.getByRole("navigation", { name: "First aid" });
  await expect(row.getByRole("button", { name: "CPR" })).toBeVisible();
  await expect(row.getByRole("button", { name: "Choking" })).toHaveCount(0);

  await page.keyboard.press("f");
  await expect(row.getByRole("button", { name: "CPR" })).toBeFocused();
  await page.keyboard.press("ArrowRight");
  await expect(row.getByRole("button", { name: "Severe bleeding" })).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(/\/portal\/read\?src=/);
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute(
    "src",
    "/library/content/mdwiki_en_all/Severe bleeding",
  );
  const frame = page.frameLocator("iframe.reader-frame");
  await expect(frame.locator("p")).toBeVisible();
  await frame.locator("p").click();
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/portal$/);
});

test("the full grid has the red note, opens an article and Back returns to the grid", async ({
  page,
}) => {
  await setStatus(page, {
    first_aid: [entry("cpr", "CPR"), entry("burns", "Burns")],
  });
  await page.goto("/portal");
  await page.getByRole("button", { name: "More …" }).click();
  await expect(page).toHaveURL(/\/portal\/first-aid$/);
  await expect(
    page.getByText(/This is not a doctor\..*Act fast in an emergency; read later\./),
  ).toBeVisible();
  await page.getByRole("button", { name: "Burns" }).click();
  await expect(page).toHaveURL(/\/portal\/read\?src=/);
  await page.getByRole("button", { name: /Back/ }).click();
  await expect(page).toHaveURL(/\/portal\/first-aid$/);
});

test("no available entry shows the missing-book message", async ({ page }) => {
  await setStatus(page, {
    first_aid: [entry("cpr", "CPR", false)],
    alerts: [
      {
        code: "LAI-SVC-109",
        severity: "warning",
        title: "A first-aid link points to a missing article",
        fix: "Report it. Use Library search meanwhile.",
      },
    ],
  });
  await page.goto("/portal/first-aid");
  await expect(
    page.getByText("The medical book is not installed. Plug in the ark drive."),
  ).toBeVisible();
  await expect(page.getByText("LAI-SVC-109")).toBeVisible();
});

test("first-aid screens have no serious accessibility violations", async ({
  page,
}) => {
  await setStatus(page, {
    first_aid: [entry("cpr", "CPR"), entry("snake_bite", "Snake bite")],
  });
  for (const path of ["/portal", "/portal/first-aid"]) {
    await page.goto(path);
    await expect(page.locator("main")).toBeVisible();
    const res = await new AxeBuilder({ page }).analyze();
    const bad = res.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    );
    expect(bad.map((v) => v.id)).toEqual([]);
  }
});
