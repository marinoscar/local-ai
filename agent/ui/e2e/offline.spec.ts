import { expect, test, type Page } from "@playwright/test";

// PORTAL-05: the Portal works with no network at all (survival-portal.md section 4.11). Every
// request whose host is not 127.0.0.1 is aborted, and the test fails if one is even attempted.

const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; worker-src 'self' blob:; frame-src 'self'; frame-ancestors 'self' localai://app; base-uri 'none'; form-action 'self'";

// The 11 client routes of survival-portal.md section 3.1, then the map viewer.
const ROUTES = [
  "/portal",
  "/portal/chat",
  "/portal/photo",
  "/portal/talk",
  "/portal/library",
  "/portal/maps",
  "/portal/manuals",
  "/portal/status",
  "/portal/brain",
  "/portal/first-aid",
  "/portal/help",
  "/maps/",
];

async function guard(page: Page) {
  const attempted: string[] = [];
  await page.route("**/*", (route) => {
    const u = new URL(route.request().url());
    if (u.hostname !== "127.0.0.1") {
      attempted.push(u.href);
      return route.abort();
    }
    return route.continue();
  });
  // A blocked resource shows up as a CSP violation too (the second line of defence).
  await page.addInitScript(() => {
    const w = window as unknown as { __csp: string[] };
    w.__csp = [];
    document.addEventListener("securitypolicyviolation", (e) =>
      w.__csp.push(`${e.violatedDirective} ${e.blockedURI}`),
    );
  });
  return attempted;
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

test("the guard really catches an outside request (the test is not blind)", async ({
  page,
}) => {
  const attempted = await guard(page);
  // A page without the Portal CSP (the fake's own JSON log): the request is not stopped by the
  // browser, only by the guard.
  await page.goto("/__seen");
  await page.evaluate(() =>
    fetch("http://203.0.113.9/x", { mode: "no-cors" }).catch(() => "blocked"),
  );
  expect(attempted).toEqual(["http://203.0.113.9/x"]);
});

test("every Portal screen and the map viewer load without any non-loopback request", async ({
  page,
}) => {
  await page.clock.install(); // no 2 s jump from the chat interstitial
  const attempted = await guard(page);
  const bad: string[] = [];
  page.on("response", (res) => {
    const u = new URL(res.url());
    const csp = res.headers()["content-security-policy"];
    // The pages carry the Portal CSP; the hashed assets are covered by the agent's middleware (Go tests).
    if (res.request().resourceType() === "document" && csp !== CSP)
      bad.push(`${u.pathname}: ${csp}`);
  });
  for (const lang of ["en", "es"]) {
    await page.addInitScript(
      (l) => window.localStorage.setItem("lai.portal.lang", l),
      lang,
    );
    for (const route of ROUTES) {
      await page.goto(route);
      await expect(page.locator("body")).not.toBeEmpty();
      if (route.startsWith("/portal"))
        await expect(page.locator("main")).toBeVisible();
      // Let fonts, CSS and the first status fetch finish.
      await page.evaluate(() => document.fonts.ready);
      await page.clock.runFor(5100);
    }
  }
  expect(attempted).toEqual([]);
  expect(bad).toEqual([]);
  const violations = await page.evaluate(
    () => (window as unknown as { __csp: string[] }).__csp,
  );
  expect(violations).toEqual([]);
});

test("no service worker is registered", async ({ page }) => {
  await guard(page);
  await page.goto("/portal");
  await expect(page.locator("main")).toBeVisible();
  const regs = await page.evaluate(
    async () => (await navigator.serviceWorker.getRegistrations()).length,
  );
  expect(regs).toBe(0);
});

test("the font is a local file", async ({ page }) => {
  const attempted = await guard(page);
  const fonts: string[] = [];
  page.on("requestfinished", (r) => {
    if (r.resourceType() === "font") fonts.push(new URL(r.url()).pathname);
  });
  await page.goto("/portal");
  await page.evaluate(() => document.fonts.ready);
  expect(fonts.length).toBeGreaterThan(0);
  expect(
    fonts.every((f) => f.startsWith("/portal/assets/") && f.endsWith(".woff2")),
  ).toBe(true);
  expect(attempted).toEqual([]);
});
