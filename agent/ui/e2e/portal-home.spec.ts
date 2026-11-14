import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// PORTAL-04: Home at 1366x768, accessibility, keyboard, language, banners (survival-portal.md
// sections 4.1, 4.3, 4.10). Runs against e2e/fake-agent.mjs serving the built dist/.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

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

test('"/" redirects to the Portal in survival mode', async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/portal$/);
});

test("Home fills 1366x768 with no scrollbar at 100 %", async ({ page }) => {
  await page.goto("/portal");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "Local AI · Survival",
  );
  await expect(page.locator(".big")).toHaveCount(8);
  const m = await page.evaluate(() => ({
    sh: document.documentElement.scrollHeight,
    ih: window.innerHeight,
    sw: document.documentElement.scrollWidth,
    iw: window.innerWidth,
  }));
  expect(m.sh).toBeLessThanOrEqual(m.ih);
  expect(m.sw).toBeLessThanOrEqual(m.iw);
  // The buttons are 300x170 at 100 %, or larger when the row is wider.
  const box = await page.locator(".big").first().boundingBox();
  expect(box!.width).toBeGreaterThanOrEqual(299);
  expect(box!.height).toBeGreaterThanOrEqual(169);
});

test("150 % text scrolls vertically, never horizontally", async ({ page }) => {
  await page.goto("/portal");
  await page.getByRole("button", { name: /150 %/ }).click();
  const m = await page.evaluate(() => ({
    sw: document.documentElement.scrollWidth,
    iw: window.innerWidth,
  }));
  expect(m.sw).toBeLessThanOrEqual(m.iw);
  await page.reload();
  expect(
    await page.evaluate(() => document.documentElement.className),
  ).toContain("scale-1_5");
});

test("status line shows ready, the brain and the emergency number", async ({
  page,
}) => {
  await page.goto("/portal");
  await expect(page.locator(".statusline")).toContainText("Ready.");
  await expect(page.locator(".statusline")).toContainText("Brain: Qwen 3.5 9B");
  await expect(page.locator(".statusline")).toContainText(
    "Emergency number: 911",
  );
  await expect(
    page.getByRole("button", { name: /Status and battery/ }),
  ).toContainText("72 %, 2 h 25 min");
});

test("status line is amber when a capability is not ready", async ({
  page,
}) => {
  await setStatus(page, {
    capabilities: {
      chat: { state: "ready" },
      library: { state: "ready" },
      photo: { state: "degraded", reason: "needs_switch" },
    },
  });
  await page.goto("/portal");
  await expect(page.locator(".dot-amber")).toBeVisible();
  await expect(page.locator(".statusline")).toContainText(
    "Ready, with problems. See Status.",
  );
});

test("status line is red when the library is unavailable", async ({ page }) => {
  await setStatus(page, {
    capabilities: {
      chat: { state: "ready" },
      library: { state: "unavailable", code: "LAI-SVC-001" },
      maps: { state: "ready" },
    },
  });
  await page.goto("/portal");
  await expect(page.locator(".dot-red")).toBeVisible();
  await expect(page.locator(".statusline")).toContainText(
    "Not ready. See Status.",
  );
});

test.describe("accessibility (axe: zero serious or critical)", () => {
  test("Home", async ({ page }) => {
    await page.goto("/portal");
    await expect(page.locator(".big")).toHaveCount(8);
    await noSeriousViolations(page);
  });

  test("Home in Spanish at 150 %", async ({ page }) => {
    await page.goto("/portal");
    await page.getByRole("button", { name: "Español" }).click();
    await page.getByRole("button", { name: /150 %/ }).click();
    await noSeriousViolations(page);
  });

  test("lock dialog", async ({ page }) => {
    await setStatus(page, { session: "none" });
    await page.goto("/portal/talk");
    await expect(page.getByRole("dialog")).toBeVisible();
    await noSeriousViolations(page);
  });

  test("unavailable screen", async ({ page }) => {
    await setStatus(page, {
      capabilities: {
        chat: { state: "ready" },
        library: { state: "unavailable", code: "LAI-SVC-001" },
      },
    });
    await page.goto("/portal/library");
    await expect(
      page.getByRole("heading", { name: "This isn't available right now" }),
    ).toBeVisible();
    await expect(page.getByText("The library has no books.")).toBeVisible();
    await expect(page.getByText("Code: LAI-SVC-001")).toBeVisible();
    await noSeriousViolations(page);
  });

  test("chat interstitial", async ({ page }) => {
    await page.clock.install();
    await page.goto("/portal/chat");
    await expect(
      page.getByRole("heading", { name: "Opening the chat …" }),
    ).toBeVisible();
    await noSeriousViolations(page);
  });
});

test("keyboard walkthrough of Home: Tab order, 1-8, F and Esc", async ({
  page,
}) => {
  await page.goto("/portal");
  await expect(page.locator(".big")).toHaveCount(8);
  const focused = () =>
    page.evaluate(() =>
      (document.activeElement?.textContent ?? "").replace(/\s+/g, " ").trim(),
    );
  const order: string[] = [];
  for (let i = 0; i < 17; i++) {
    await page.keyboard.press("Tab");
    order.push(await focused());
  }
  expect(order[0]).toContain("Skip");
  expect(order[1]).toBe("English");
  expect(order[2]).toBe("Español");
  expect(order.slice(3, 6)).toEqual(["A", "A+", "A++"]);
  // The status line's Help button (dashboard.md section 4.1) comes before the big buttons.
  expect(order[6]).toBe("Help");
  const bigs = order.slice(7, 15);
  [
    "Chat",
    "Ask about a photo",
    "Talk",
    "Library",
    "Maps",
    "Manuals",
    "Status and battery",
    "Switch brain",
  ].forEach((name, i) => expect(bigs[i]).toContain(name));
  expect(order[15]).toBe("CPR");

  await page.goto("/portal");
  await page.keyboard.press("f");
  expect(await focused()).toBe("CPR");

  const targets = [
    "chat",
    "photo",
    "talk",
    "library",
    "maps",
    "manuals",
    "status",
    "brain",
  ];
  for (let n = 1; n <= 8; n++) {
    await page.goto("/portal");
    await page.locator(".big").first().waitFor();
    await page.keyboard.press(String(n));
    await expect(page).toHaveURL(new RegExp(`/portal/${targets[n - 1]}$`));
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/portal$/);
  }
});

test("Enter and Space activate a focused button", async ({ page }) => {
  await page.goto("/portal");
  await page.getByRole("button", { name: /^6.*Manuals|Manuals/ }).focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/portal\/manuals$/);
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: /Maps/ }).focus();
  await page.keyboard.press("Space");
  await expect(page).toHaveURL(/\/portal\/maps$/);
});

test("Ctrl + and Ctrl - change the text scale", async ({ page }) => {
  await page.goto("/portal");
  await page.keyboard.press("Control+=");
  expect(
    await page.evaluate(() => document.documentElement.className),
  ).toContain("scale-1_25");
  await page.keyboard.press("Control+-");
  expect(
    await page.evaluate(() => document.documentElement.className),
  ).toContain("scale-1");
});

test("deep links work and unknown screens fall back to Home", async ({
  page,
}) => {
  await page.goto("/portal/manuals");
  await expect(page.getByRole("heading", { name: "Manuals" })).toBeVisible();
  await page.goto("/portal/nothing-here");
  await expect(page.locator(".big")).toHaveCount(8);
});

test("language toggle switches instantly, persists, and every API request carries the language", async ({
  page,
}) => {
  await page.goto("/portal");
  await expect(page.getByRole("button", { name: /Chat/ })).toBeVisible();
  await page.getByRole("button", { name: "Español" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "IA Local · Supervivencia",
  );
  await expect(page.getByRole("button", { name: /Conversar/ })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.lang)).toBe("es");
  await page.reload();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "IA Local · Supervivencia",
  );
  await page.waitForTimeout(300);
  const seen: { lang: string | null }[] = await (
    await page.request.get("/__seen")
  ).json();
  expect(seen.length).toBeGreaterThan(0);
  expect(seen.at(-1)!.lang).toBe("es");
  expect(seen.every((s) => s.lang === "en" || s.lang === "es")).toBe(true);
});

test("the agent language is used until a choice is made", async ({ page }) => {
  await setStatus(page, { language: "es" });
  await page.goto("/portal");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "IA Local · Supervivencia",
  );
});

test.describe("banners", () => {
  test("survival mode off: banner under the status line, Start survival mode works", async ({
    page,
  }) => {
    await setStatus(page, { mode: "dashboard", session: "none" });
    await page.goto("/portal");
    await expect(
      page.locator(".banner-text", { hasText: "Survival mode is off." }),
    ).toBeVisible();
    // dashboard.md section 4.1: the status line is shown in every mode.
    await expect(page.locator(".statusline")).toContainText("Ready.");
    await page.getByRole("button", { name: "Start survival mode" }).click();
    await expect(
      page.locator(".banner-text", { hasText: "Survival mode is off." }),
    ).toHaveCount(0);
    await expect(page.locator(".statusline")).toContainText("Ready.");
  });

  test("locked buttons open the lock dialog with Start survival mode and Log in", async ({
    page,
  }) => {
    await setStatus(page, { mode: "dashboard", session: "none" });
    await page.goto("/portal");
    await page.getByRole("button", { name: /Switch brain/ }).click();
    const dlg = page.getByRole("dialog");
    await expect(dlg).toBeVisible();
    await expect(dlg).toContainText(
      "To use this, start Survival mode or log in.",
    );
    await expect(
      dlg.getByRole("button", { name: "Start survival mode" }),
    ).toBeVisible();
    await expect(dlg.getByRole("button", { name: "Log in" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/portal$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("Library and Maps are not locked without a session", async ({
    page,
  }) => {
    await setStatus(page, { session: "none" });
    await page.goto("/portal");
    await page.getByRole("button", { name: /Library/ }).click();
    await expect(page).toHaveURL(/\/portal\/library$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("lost contact shows the banner, retries, and recovers; Library still opens", async ({
    page,
  }) => {
    await page.clock.install();
    await page.goto("/portal");
    await expect(page.locator(".big")).toHaveCount(8);
    await page.request.get("/__down?on=1");
    await page.clock.runFor(5000);
    await expect(
      page.locator(".banner-text", {
        hasText: "Lost contact with the AI service.",
      }),
    ).toBeVisible();
    // A code and one next step that works with the agent down (#430).
    await expect(
      page.locator(".banner-note", { hasText: "restart the computer" }),
    ).toContainText("Code: LAI-SVC-116");
    await page.getByRole("button", { name: /Library/ }).click();
    await expect(page).toHaveURL(/\/portal\/library$/);
    await page.request.get("/__down?on=0");
    await page.clock.runFor(5000);
    await expect(
      page.locator(".banner-text", { hasText: "Lost contact" }),
    ).toHaveCount(0);
  });

  test("battery low at 20 % and critical at 10 %", async ({ page }) => {
    await page.clock.install();
    await page.goto("/portal");
    await expect(page.locator(".big")).toHaveCount(8);
    const power = (pct: number) => ({
      on_ac: false,
      battery_pct: pct,
      minutes_left: 20,
      state: "discharging",
      low: pct <= 20,
      critical: pct <= 10,
    });
    await setStatus(page, { power: power(20) });
    await page.clock.runFor(5000);
    await expect(
      page.locator(".banner-text", { hasText: "Battery low (20 %)" }),
    ).toBeVisible();
    await setStatus(page, { power: power(10) });
    await page.clock.runFor(5000);
    await expect(
      page.locator(".banner-text", { hasText: "Battery very low" }),
    ).toBeVisible();
    await noSeriousViolations(page);
    await setStatus(page, { power: { ...power(10), on_ac: true } });
    await page.clock.runFor(5000);
    await expect(
      page.locator(".banner-text", { hasText: "Battery very low" }),
    ).toHaveCount(0);
  });
});

test("chat opens /chat/ in the reader frame after 2 s or on a key, with Home always on screen, and offers a brain when none runs", async ({
  page,
}) => {
  const inReader = /\/portal\/read\?src=%2Fchat%2F&back=%2Fportal$/;
  await page.clock.install();
  await page.goto("/portal/chat");
  await page.clock.runFor(2100);
  await expect(page).toHaveURL(inReader);
  await expect(page.locator("iframe.reader-frame")).toHaveAttribute("src", "/chat/");
  // Issue #431: the way back is on screen, not only the browser's Back button.
  await page.getByRole("button", { name: "⌂ Home" }).click();
  await expect(page).toHaveURL(/\/portal\/?$/);

  await page.goto("/portal/chat");
  await expect(
    page.getByRole("heading", { name: "Opening the chat …" }),
  ).toBeVisible();
  await page.keyboard.press("x");
  await expect(page).toHaveURL(inReader);
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/portal\/?$/);

  await setStatus(page, { brain: { loaded: [], loading: null, choices: [] } });
  await page.goto("/portal/chat");
  await expect(
    page.getByRole("heading", { name: "No brain is running." }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Choose a brain" }).click();
  await expect(page).toHaveURL(/\/portal\/brain$/);
});
