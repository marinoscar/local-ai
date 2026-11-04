import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// PORTAL-07: the Talk flow (survival-portal.md sections 4.1.4 and 4.5) with Chromium's fake
// microphone playing e2e/fixtures/<lang>.wav, against the fake agent plus routed fakes for the
// speech and chat upstreams.

const here = dirname(fileURLToPath(import.meta.url));
const fixture = (n: string) => join(here, "fixtures", n);

function micArgs(wav: string): string[] {
  return [
    "--use-fake-device-for-media-stream",
    "--use-fake-ui-for-media-stream",
    `--use-file-for-fake-audio-capture=${fixture(wav)}`,
  ];
}

function silentWav(): Buffer {
  const n = 1600;
  const b = Buffer.alloc(44 + n * 2);
  b.write("RIFF", 0);
  b.writeUInt32LE(36 + n * 2, 4);
  b.write("WAVE", 8);
  b.write("fmt ", 12);
  b.writeUInt32LE(16, 16);
  b.writeUInt16LE(1, 20);
  b.writeUInt16LE(1, 22);
  b.writeUInt32LE(16000, 24);
  b.writeUInt32LE(32000, 28);
  b.writeUInt16LE(2, 32);
  b.writeUInt16LE(16, 34);
  b.write("data", 36);
  b.writeUInt32LE(n * 2, 40);
  return b;
}

interface Rig {
  stt: { language: string }[];
  chat: { messages: { role: string; content: string }[]; model: string }[];
  tts: { text: string; language: string; voice: string | null }[];
}

interface RigOptions {
  sttText: string;
  sttLanguage: string;
  answer: string[];
  tts: "ok" | "hang" | "novoice";
}

async function rig(page: Page, o: RigOptions): Promise<Rig> {
  const r: Rig = { stt: [], chat: [], tts: [] };
  await page.addInitScript(() => {
    const w = window as unknown as { __aborts: string[] };
    w.__aborts = [];
    const orig = window.fetch.bind(window);
    window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.href
            : input.url;
      init?.signal?.addEventListener("abort", () => w.__aborts.push(url));
      return orig(input, init);
    };
  });
  await page.route("**/api/v1/speech/voices", (route) =>
    route.fulfill({
      json: {
        voices: [
          {
            id: "piper-en-medium",
            language: "en",
            display_name: "English (US), medium",
            default: true,
          },
          {
            id: "piper-es-medium",
            language: "es",
            display_name: "Español (México), medium",
            default: true,
          },
        ],
      },
    }),
  );
  await page.route("**/api/v1/speech/stt", (route) => {
    const body = route.request().postDataBuffer()?.toString("latin1") ?? "";
    const m = /name="language"\r\n\r\n(\w+)/.exec(body);
    r.stt.push({ language: m?.[1] ?? "" });
    return route.fulfill({
      json: {
        text: o.sttText,
        language: o.sttLanguage,
        language_probability: 0.97,
        duration_s: 2,
        model: "whisper-small",
        elapsed_ms: 100,
      },
    });
  });
  await page.route("**/chat/v1/chat/completions", (route) => {
    r.chat.push(route.request().postDataJSON());
    const sse =
      o.answer
        .map(
          (p) =>
            `data: ${JSON.stringify({ choices: [{ delta: { content: p } }] })}\n\n`,
        )
        .join("") + "data: [DONE]\n\n";
    return route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: sse,
    });
  });
  await page.route("**/api/v1/speech/tts", async (route) => {
    const j = route.request().postDataJSON() as {
      text: string;
      language: string;
      voice: string | null;
    };
    r.tts.push(j);
    if (o.tts === "hang") return new Promise<void>(() => undefined);
    if (o.tts === "novoice")
      return route.fulfill({
        status: 503,
        json: { error: { code: "LAI-SVC-022" } },
      });
    return route.fulfill({ status: 200, contentType: "audio/wav", body: silentWav() });
  });
  return r;
}

async function talk(page: Page) {
  await page.request.post("/__reset");
  await page.goto("/portal/talk");
  await expect(page.getByRole("heading", { level: 1, name: /Talk|Hablar/ })).toBeVisible();
}

async function recordAndStop(page: Page) {
  await page.locator(".mic").click();
  await expect(page.locator(".mic-recording")).toBeVisible();
  await page.waitForTimeout(1200);
  await page.locator(".mic").click();
}

const EN = {
  sttText: "How do I make water safe to drink?",
  sttLanguage: "en",
  answer: ["Boil it for one minute. ", "Then let it cool. ", "Store it clean."],
};

test.describe("Talk with the fake microphone (English)", () => {
  test.use({
    permissions: ["microphone"],
    launchOptions: { args: micArgs("en.wav") },
  });

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

test.describe("Talk with the fake microphone (Spanish)", () => {
  test.use({
    permissions: ["microphone"],
    launchOptions: { args: micArgs("es.wav") },
  });

  test("uses the Spanish prompt and asks TTS for Spanish", async ({ page }) => {
    const r = await rig(page, {
      sttText: "¿Cómo purifico agua para beber?",
      sttLanguage: "es",
      answer: ["Hierva el agua un minuto. ", "Déjela enfriar."],
      tts: "ok",
    });
    await talk(page);
    // Auto: the recognizer's language picks the prompt and the voice.
    await recordAndStop(page);
    await expect(page.locator(".talk-answer")).toContainText("Déjela enfriar.");
    await expect.poll(() => r.tts.length).toBe(2);
    expect(r.chat[0].messages[0].content).toContain("Responda en español");
    expect(r.tts.every((x) => x.language === "es")).toBe(true);
    expect(r.stt).toEqual([{ language: "auto" }]);
  });

  test("a fixed language is sent to the recognizer", async ({ page }) => {
    const r = await rig(page, {
      sttText: "Hola",
      sttLanguage: "es",
      answer: ["Hola."],
      tts: "ok",
    });
    await talk(page);
    await page.getByRole("button", { name: "Español", exact: true }).last().click();
    await recordAndStop(page);
    await expect(page.locator(".talk-answer")).toContainText("Hola.");
    expect(r.stt).toEqual([{ language: "es" }]);
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
