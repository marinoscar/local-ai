import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// PORTAL-09: Library search and the reader frame (survival-portal.md section 4.1.5). Runs against
// e2e/fake-agent.mjs, which answers /library/search with an OpenSearch RSS like kiwix-serve.
// The real-kiwix run with the test ZIMs is the manual test of the issue.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
  await setStatus(page, {
    capabilities: {
      chat: { state: "ready" },
      photo: { state: "ready" },
      talk: { state: "ready" },
      library: {
        state: "ready",
        books: [
          {
            content_id: "wikipedia-en",
            title: "Wikipedia (English)",
            book: "wikipedia_en_all",
            lang: "en",
          },
        ],
      },
      maps: { state: "ready" },
      manuals: { state: "ready" },
    },
  });
});

test("search, open a result, Back to results, Esc from inside the frame", async ({
  page,
}) => {
  await page.goto("/portal/library");
  await page.getByRole("searchbox").fill("water purification");
  await page.getByRole("searchbox").press("Enter");
  await expect(page.getByRole("heading", { name: "Results (2)" })).toBeVisible();
  const seen = await (await page.request.get("/__seen")).json();
  expect(
    seen.some((s: { path: string }) => s.path.includes("books.filter.lang=eng")),
  ).toBe(true);

  await page
    .getByRole("link", { name: "Water purification — Wikipedia" })
    .click();
  await expect(page).toHaveURL(/\/portal\/read\?src=/);
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute(
    "src",
    "/library/content/wikipedia_en_all/Water_purification",
  );

  await page.getByRole("button", { name: /Back to results/ }).click();
  await expect(page.getByRole("searchbox")).toHaveValue("water purification");
  await expect(page.getByRole("heading", { name: "Results (2)" })).toBeVisible();

  // Esc while the focus is inside the frame returns home.
  await page
    .getByRole("link", { name: "Water purification — Wikipedia" })
    .click();
  const frame = page.frameLocator("iframe.reader-frame");
  await expect(frame.locator("p")).toBeVisible();
  await frame.locator("p").click();
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/portal$/);
});

test("All languages removes the filter and no results shows the message", async ({
  page,
}) => {
  await page.goto("/portal/library");
  await page.getByLabel("All languages").check();
  await page.getByRole("searchbox").fill("x");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Results (2)" })).toBeVisible();
  const seen = await (await page.request.get("/__seen")).json();
  const last = seen.filter((s: { path: string }) =>
    s.path.startsWith("/library/search"),
  );
  expect(last[last.length - 1].path).not.toContain("books.filter.lang");

  await page.getByRole("searchbox").fill("nothing");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(
    page.getByText("Nothing found. Try other words."),
  ).toBeVisible();
});

test("a book button opens the undated book in the reader", async ({ page }) => {
  await page.goto("/portal/library");
  await page.getByRole("button", { name: "Wikipedia (English)" }).click();
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute(
    "src",
    "/library/content/wikipedia_en_all/",
  );
});

test("the reader refuses a source outside the allowlist", async ({ page }) => {
  await page.goto("/portal/read?src=https://evil.invalid/library/&back=/portal");
  await expect(page).toHaveURL(/\/portal$/);
  await page.goto("/portal/read?src=/api/v1/portal/status");
  await expect(page).toHaveURL(/\/portal$/);
});

test("an unavailable library explains itself", async ({ page }) => {
  await setStatus(page, {
    capabilities: {
      chat: { state: "ready" },
      library: { state: "unavailable", code: "LAI-SVC-001" },
    },
  });
  await page.goto("/portal/library");
  await expect(page.getByText("LAI-SVC-001")).toBeVisible();
});

test("library and reader have no serious accessibility violations", async ({
  page,
}) => {
  for (const path of [
    "/portal/library",
    "/portal/read?src=/library/content/wikipedia_en_all/&back=/portal/library",
  ]) {
    await page.goto(path);
    await expect(page.locator("main")).toBeVisible();
    const res = await new AxeBuilder({ page }).analyze();
    const bad = res.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    );
    expect(bad.map((v) => v.id)).toEqual([]);
  }
});
