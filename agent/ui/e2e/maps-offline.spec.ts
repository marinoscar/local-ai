import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// SVC-10: the offline map viewer at /maps/ (companion-services.md section 4.4.3). Runs against
// e2e/fake-agent.mjs (fixture PMTiles, catalog and place routes) serving the built dist/.

async function fixture(page: Page, body: Record<string, unknown> = {}) {
  await page.request.post("/__maps", { data: body });
}

/** Aborts every request that is not for 127.0.0.1 and records it: the test fails if any was tried. */
async function blockExternal(page: Page): Promise<string[]> {
  const attempts: string[] = [];
  await page.route("**/*", (route) => {
    const u = new URL(route.request().url());
    if (u.hostname !== "127.0.0.1" && u.protocol !== "data:" && u.protocol !== "blob:") {
      attempts.push(route.request().url());
      return route.abort();
    }
    return route.continue();
  });
  return attempts;
}

async function canvasPainted(page: Page) {
  await page.waitForFunction(() => {
    const c = document.querySelector<HTMLCanvasElement>("canvas.maplibregl-canvas");
    if (!c || c.width === 0) return false;
    const copy = document.createElement("canvas");
    copy.width = c.width;
    copy.height = c.height;
    const ctx = copy.getContext("2d");
    if (!ctx) return false;
    ctx.drawImage(c, 0, 0);
    const px = ctx.getImageData(Math.floor(c.width / 2), Math.floor(c.height / 2), 1, 1).data;
    return px[3] === 255 && px[0] + px[1] + px[2] > 0;
  });
}

test.beforeEach(async ({ page }) => {
  await fixture(page);
});

test("opens with every non-127.0.0.1 request aborted, none attempted, and the map paints", async ({ page }) => {
  const external = await blockExternal(page);
  const violations: string[] = [];
  page.on("console", (m) => {
    if (/content security policy|refused to/i.test(m.text())) violations.push(m.text());
  });
  const res = await page.goto("/maps/");
  expect(res?.headers()["content-security-policy"]).toContain("default-src 'self'");
  await expect(page.locator("canvas.maplibregl-canvas")).toBeVisible();
  await canvasPainted(page);
  await expect(page.getByTestId("coords-decimal")).toHaveText(/^-?\d+\.\d{5}, -?\d+\.\d{5}$/);
  expect(external).toEqual([]);
  expect(violations).toEqual([]);
});

test("without map labels it hides the labels and shows the banner (LAI-SVC-031)", async ({ page }) => {
  const asked: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/maps/assets/")) asked.push(r.url());
  });
  const cat = await (await page.request.get("/maps/catalog.json")).json();
  cat.assets = { available: false };
  await fixture(page, { catalog: cat });
  await page.goto("/maps/");
  await expect(page.getByText("Map labels are missing. Names of places can't be shown.")).toBeVisible();
  await expect(page.getByText("LAI-SVC-031")).toBeVisible();
  await canvasPainted(page);
  expect(asked).toEqual([]);
});

test("with labels available it asks the agent for the sprite", async ({ page }) => {
  const asked: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/maps/assets/sprites/")) asked.push(r.url());
  });
  await page.goto("/maps/");
  await canvasPainted(page);
  await expect.poll(() => asked.length).toBeGreaterThan(0);
  await expect(page.getByText("Map labels are missing")).toHaveCount(0);
});

test("no map installed shows LAI-SVC-030 with its fix", async ({ page }) => {
  const cat = await (await page.request.get("/maps/catalog.json")).json();
  cat.maps = [];
  await fixture(page, { catalog: cat });
  await page.goto("/maps/");
  await expect(page.getByText("No offline map is installed.")).toBeVisible();
  await expect(page.getByText("aictl ark import")).toBeVisible();
  await expect(page.getByText("LAI-SVC-030")).toBeVisible();
});

test("the picker switches maps and the view follows", async ({ page }) => {
  await page.goto("/maps/");
  await canvasPainted(page);
  await expect(page.getByTestId("coords-decimal")).toHaveText(/^-5\.0/);
  await page.locator("select").selectOption("map-b");
  await expect(page.getByTestId("coords-decimal")).toHaveText(/^5\.0/, { timeout: 10_000 });
});

test("leaving the map bounds suggests the map that holds the view", async ({ page }) => {
  await page.goto("/maps/");
  await canvasPainted(page);
  await page.getByRole("searchbox").fill("5.1, 5.2");
  await page.getByRole("button", { name: "Search" }).click();
  const suggest = page.locator(".suggest");
  await expect(suggest).toContainText("Map B", { timeout: 10_000 });
  await suggest.getByRole("button", { name: "Switch map" }).click();
  await expect(page.locator("select")).toHaveValue("map-b");
  await expect(suggest).toHaveCount(0);
});

test("the language toggle switches the screen between English and Spanish", async ({ page }) => {
  await page.goto("/maps/");
  await expect(page.getByRole("button", { name: "Search" })).toBeVisible();
  await page.getByRole("button", { name: "Español" }).click();
  await expect(page.getByRole("button", { name: "Buscar" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Dónde estoy" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "es");
  await expect(page.locator("select")).toContainText("(es)");
  await page.getByRole("button", { name: "English" }).click();
  await expect(page.getByRole("button", { name: "Search" })).toBeVisible();
});

test("search, nearest, where am I and I am here work with the keyboard buttons", async ({ page }) => {
  await page.goto("/maps/");
  await canvasPainted(page);

  await page.getByRole("button", { name: "Where am I" }).click();
  await expect(page.getByText("No GPS. Tap the map where you are")).toBeVisible();

  await page.getByRole("button", { name: "hospital" }).click();
  await expect(page.getByText("Use Where am I first")).toBeVisible();

  await page.getByRole("button", { name: "I am here" }).click();
  await expect(page.getByText("Your position is saved.")).toBeVisible();

  await page.getByRole("button", { name: "Where am I" }).click();
  await expect(page.getByText("Position you set by hand.")).toBeVisible();

  await page.getByRole("button", { name: "hospital" }).click();
  await expect(page.getByRole("button", { name: /Fixture Hospital/ })).toBeVisible();

  await page.getByRole("searchbox").fill("Fixture");
  await page.getByRole("searchbox").press("Enter");
  await expect(page.getByRole("button", { name: /Fixture Town/ })).toBeVisible();
  await expect(page.getByText("GeoNames (CC BY 4.0)").first()).toBeVisible();
});

test("search is disabled with a reason when no place index is installed", async ({ page }) => {
  const cat = await (await page.request.get("/maps/catalog.json")).json();
  cat.search = { available: false };
  await fixture(page, { catalog: cat });
  await page.goto("/maps/");
  await expect(page.getByRole("searchbox")).toBeDisabled();
  await expect(page.getByText("Place search is not installed.")).toBeVisible();
  await expect(page.getByText("LAI-SVC-032")).toBeVisible();
});

test("copy coordinates shows decimal and DMS", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto("/maps/");
  await canvasPainted(page);
  await expect(page.getByTestId("coords-dms")).toHaveText(/^\d+°\d+'\d+"[NS] \d+°\d+'\d+"[EW]$/);
  const shown = await page.getByTestId("coords-decimal").innerText();
  await page.getByRole("button", { name: "Copy coordinates" }).click();
  await expect(page.getByText("Copied.")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(shown);
});

test("Tab reaches every control and + and - zoom the map", async ({ page }) => {
  await page.goto("/maps/");
  await canvasPainted(page);
  const names = [
    "Español",
    "Search",
    "Where am I",
    "I am here",
    "hospital",
    "clinic",
    "water",
    "town",
    "Zoom in",
    "Zoom out",
    "Copy coordinates",
  ];
  for (const n of names) {
    await expect(page.getByRole("button", { name: n, exact: true })).toBeEnabled();
  }
  // Walk the page with Tab and collect what gets focus.
  const seen = new Set<string>();
  await page.locator("body").press("Tab");
  for (let i = 0; i < 40; i++) {
    const label = await page.evaluate(() => {
      const e = document.activeElement as HTMLElement | null;
      return e ? (e.getAttribute("aria-label") ?? e.textContent ?? e.tagName).trim() : "";
    });
    seen.add(label);
    await page.keyboard.press("Tab");
  }
  for (const n of ["Where am I", "I am here", "Zoom in", "Zoom out", "Copy coordinates", "Search"])
    expect([...seen].some((s) => s.startsWith(n)), `${n} not reachable by Tab`).toBe(true);

  const scale = page.locator(".maplibregl-ctrl-scale");
  const before = await scale.innerText();
  await page.getByRole("button", { name: "Zoom in", exact: true }).click();
  await expect.poll(() => scale.innerText()).not.toBe(before);
  const mid = await scale.innerText();
  await page.getByRole("heading", { level: 1 }).click();
  await page.keyboard.press("-");
  await expect.poll(() => scale.innerText()).not.toBe(mid);
});

test("axe-core finds no serious violation", async ({ page }) => {
  await page.goto("/maps/");
  await canvasPainted(page);
  const res = await new AxeBuilder({ page }).analyze();
  const bad = res.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
});
