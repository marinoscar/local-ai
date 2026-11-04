import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// PORTAL-10: Maps and Manuals screens and the built-in emergency page (survival-portal.md sections
// 4.1.6, 4.1.7, 4.8). Runs against e2e/fake-agent.mjs serving the built dist/.

const ITEMS = [
  { id: "03-network", title: "Network", url: "/docs/03-network.html" },
  { id: "07-troubleshooting", title: "Troubleshooting", url: "/docs/07-troubleshooting.html" },
  { id: "01-survival-mode", title: "Survival mode", url: "/docs/01-survival-mode.html" },
  { id: "06-rebuild-offline", title: "Rebuild offline", url: "/docs/06-rebuild-offline.html" },
];

async function setCaps(page: Page, caps: Record<string, unknown>) {
  const cur = await (await page.request.get("/api/v1/portal/status")).json();
  await page.request.post("/__status", {
    data: { capabilities: { ...cur.capabilities, ...caps } },
  });
}

async function noSeriousViolations(page: Page) {
  const res = await new AxeBuilder({ page }).analyze();
  const bad = res.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
  await page.request.post("/__maps", { data: {} });
});

test("with no manuals on disk the Manuals button opens the built-in help page", async ({ page }) => {
  await setCaps(page, { manuals: { state: "unavailable", code: "LAI-SVC-109", reason: "no_docs" } });
  await page.goto("/portal");
  await page.getByRole("button", { name: /Manuals/ }).click();
  await expect(page).toHaveURL(/\/portal\/help$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Emergency help");
  await expect(page.getByText("aictl mode survival")).toBeVisible();
  await expect(page.getByText("aictl ark import")).toBeVisible();
  await expect(page.getByText("http://127.0.0.1:8888/library/")).toBeVisible();
  await expect(page.getByText("LAI-SVC-001")).toBeVisible();
  await expect(page.locator(".help-emergency")).toHaveText("Emergency number: 911");
  await expect(page.getByText("If nothing works: restart the computer and wait 2 minutes.")).toBeVisible();
  await noSeriousViolations(page);
});

test("the help page is fully translated", async ({ page }) => {
  await setCaps(page, { manuals: { state: "unavailable" } });
  await page.goto("/portal/help");
  await page.getByRole("button", { name: "Español" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Ayuda de emergencia");
  await expect(page.locator(".help-emergency")).toHaveText("Número de emergencias: 911");
  await expect(page.getByText("Si nada funciona: reinicie la computadora y espere 2 minutos.")).toBeVisible();
  await expect(page.getByText("To start survival mode")).toHaveCount(0);
  await noSeriousViolations(page);
});

test("the help page fetches nothing and works with the agent down", async ({ page }) => {
  await page.goto("/portal/help");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Emergency help");
  await page.request.get("/__down?on=1");
  const seen: string[] = [];
  page.on("request", (r) => seen.push(new URL(r.url()).pathname));
  await page.waitForTimeout(500);
  await expect(page.getByText("If nothing works")).toBeVisible();
  // Only the Portal's own status poll may run; the page itself requests nothing else.
  expect(seen.filter((p) => !p.startsWith("/api/v1/portal/status") && !p.startsWith("/api/v1/events"))).toEqual([]);
});

test("unavailable screens link to the help page", async ({ page }) => {
  await setCaps(page, { maps: { state: "unavailable", code: "LAI-SVC-030", reason: "no_map" } });
  await page.goto("/portal/maps");
  await expect(page.getByText("No offline map is installed. Plug in the ark drive.")).toBeVisible();
  await expect(page.getByText("Code: LAI-SVC-030")).toBeVisible();
  await page.getByRole("button", { name: "Emergency help" }).click();
  await expect(page).toHaveURL(/\/portal\/help$/);
});

test("Manuals lists the items in order, opens them in the reader, Esc goes home", async ({ page }) => {
  await setCaps(page, { manuals: { state: "ready", items: ITEMS } });
  await page.goto("/portal/manuals");
  const names = await page.locator(".manual-btn").allInnerTexts();
  expect(names).toEqual([
    "Survival mode",
    "Rebuild offline",
    "Troubleshooting",
    "Network",
    "Print the Survival card",
    "All manuals …",
  ]);
  await noSeriousViolations(page);

  await page.getByRole("button", { name: "Survival mode" }).click();
  await expect(page).toHaveURL(/\/portal\/read\?/);
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute("src", "/docs/01-survival-mode.html");
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/portal$/);
});

test("All manuals opens /docs/ in the reader", async ({ page }) => {
  await setCaps(page, { manuals: { state: "ready", items: ITEMS } });
  await page.goto("/portal/manuals");
  await page.getByRole("button", { name: "All manuals …" }).click();
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute("src", "/docs/");
});

test("the card button opens the PDF for this node, or the HTML card when the PDF is missing", async ({ page }) => {
  await setCaps(page, { manuals: { state: "ready", items: ITEMS } });
  await page.goto("/portal/manuals");
  await page.getByRole("button", { name: "Print the Survival card" }).click();
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute("src", "/docs/survival-card-ai-n3.pdf");

  await page.route("**/docs/survival-card-ai-n3.pdf", (r) => r.fulfill({ status: 404, body: "no" }));
  await page.goto("/portal/manuals");
  await page.getByRole("button", { name: "Print the Survival card" }).click();
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute("src", "/docs/survival-card-ai-n3.html");
});

test("degraded manuals show the placeholder note", async ({ page }) => {
  await setCaps(page, { manuals: { state: "degraded", code: "LAI-SYN-062", items: ITEMS } });
  await page.goto("/portal/manuals");
  await expect(page.getByRole("note")).toContainText("Addresses appear as placeholders");
});

test("Maps opens the viewer in the reader frame and Esc goes home", async ({ page }) => {
  await page.goto("/portal");
  await page.getByRole("button", { name: /Maps/ }).click();
  await expect(page).toHaveURL(/\/portal\/read\?|\/portal\/maps$/);
  const frame = page.locator("iframe.reader-frame");
  await expect(frame).toHaveAttribute("src", "/maps/");
  await expect(page.frameLocator("iframe.reader-frame").locator("canvas.maplibregl-canvas")).toBeVisible();
  await noSeriousViolations(page);
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/portal$/);
});
