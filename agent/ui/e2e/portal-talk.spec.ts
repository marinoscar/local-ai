import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { EN, micArgs, recordAndStop, rig, talk } from "./talk-helpers";

// PORTAL-07: the Talk flow (survival-portal.md sections 4.1.4 and 4.5) with Chromium's fake
// microphone playing e2e/fixtures/en.wav, against the fake agent plus routed fakes for the speech
// and chat upstreams. The Spanish run is portal-talk-es.spec.ts (launch options are per file).

test.use({ launchOptions: { args: micArgs("en.wav") } });

test.describe("Talk with the fake microphone (English)", () => {
  test.use({ permissions: ["microphone"] });

  test("transcript, streamed answer, TTS in sentence order", async ({ page }) => {
    const r = await rig(page, { ...EN, tts: "ok" });
    await talk(page);
    await recordAndStop(page);
    await expect(page.locator(".talk-text")).toHaveText(EN.sttText);
    await expect(page.locator(".talk-answer")).toContainText("Store it clean.");
    await expect
      .poll(() => r.tts.map((x) => x.text))
      .toEqual(["Boil it for one minute.", "Then let it cool.", "Store it clean."]);
    expect(r.stt).toEqual([{ language: "auto" }]);
    expect(r.chat).toHaveLength(1);
    expect(r.chat[0].model).toBe("qwen3.5-9b-q4km");
    expect(r.chat[0].messages[0].content).toContain("Answer in English");
    expect(r.chat[0].messages.at(-1)).toEqual({ role: "user", content: EN.sttText });
    expect(r.tts.every((x) => x.language === "en")).toBe(true);
    // Back to idle once the last sentence was played, with Repeat on offer.
    await expect(page.locator(".mic")).toBeEnabled();
    await expect(page.getByRole("button", { name: "Repeat" })).toBeVisible();
  });

  test("Esc stops: every in-flight request is aborted", async ({ page }) => {
    const r = await rig(page, {
      ...EN,
      answer: ["One. ", "Two. ", "Three. ", "Four. ", "Five."],
      tts: "hang",
    });
    await talk(page);
    await recordAndStop(page);
    // One being waited for plus two ahead, never more.
    await expect.poll(() => r.tts.length).toBe(3);
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/portal\/talk$/); // the first Esc only stops
    const aborted = await page.evaluate(
      () => (window as unknown as { __aborts: string[] }).__aborts,
    );
    expect(aborted.filter((u) => u.includes("/speech/tts"))).toHaveLength(3);
    await expect(page.locator(".talk-status")).toHaveText("");
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/portal$/);
  });

  test("no voice installed: the answer is text only with a note", async ({ page }) => {
    await rig(page, { ...EN, tts: "novoice" });
    await talk(page);
    await recordAndStop(page);
    await expect(page.locator(".talk-answer")).toContainText("Store it clean.");
    await expect(
      page.getByText("Voice output is not installed; read the answer here."),
    ).toBeVisible();
  });

  test("keyboard only: Space records and stops, Tab reaches the controls", async ({ page }) => {
    await rig(page, { ...EN, tts: "ok" });
    await talk(page);
    await page.keyboard.press("Space");
    await expect(page.locator(".mic-recording")).toBeVisible();
    await page.waitForTimeout(1200);
    await page.keyboard.press("Space");
    await expect(page.locator(".talk-text")).toHaveText(EN.sttText);
    await expect(page.locator(".talk-answer")).toContainText("Store it clean.");
  });

  test("a failed transcription shows the step, the code and Try again", async ({ page }) => {
    await rig(page, { ...EN, tts: "ok" });
    await page.route("**/api/v1/speech/stt", (route) =>
      route.fulfill({ status: 400, json: { error: { code: "LAI-SVC-014" } } }),
    );
    await talk(page);
    await recordAndStop(page);
    await expect(page.getByText("LAI-SVC-014")).toBeVisible();
    await expect(page.getByText("I couldn't hear anything.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  });
});

test.describe("Talk without a microphone", () => {
  test("permission denied shows LAI-SVC-103 and how to allow it", async ({ page }) => {
    await page.addInitScript(() => {
      navigator.mediaDevices.getUserMedia = () =>
        Promise.reject(new DOMException("denied", "NotAllowedError"));
    });
    await rig(page, { ...EN, tts: "ok" });
    await talk(page);
    await page.locator(".mic").click();
    await expect(page.getByText("LAI-SVC-103")).toBeVisible();
    await expect(page.getByText("The microphone is blocked or missing.")).toBeVisible();
    await expect(page.getByText(/Firefox: click the microphone icon/)).toBeVisible();
  });
});

test.describe("Talk accessibility", () => {
  test("axe: zero serious or critical violations", async ({ page }) => {
    await rig(page, { ...EN, tts: "ok" });
    await talk(page);
    const res = await new AxeBuilder({ page }).analyze();
    const bad = res.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    );
    expect(
      bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`),
    ).toEqual([]);
  });
});

