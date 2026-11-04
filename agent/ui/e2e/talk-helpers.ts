import { expect, type Page } from "@playwright/test";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Shared fakes of the Talk specs: the routed speech and chat upstreams and the record helpers.

const here = dirname(fileURLToPath(import.meta.url));
const fixture = (n: string) => join(here, "fixtures", n);

export function micArgs(wav: string): string[] {
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

export interface Rig {
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

export async function rig(page: Page, o: RigOptions): Promise<Rig> {
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

export async function talk(page: Page) {
  await page.request.post("/__reset");
  await page.goto("/portal/talk");
  await expect(page.getByRole("heading", { level: 1, name: /Talk|Hablar/ })).toBeVisible();
}

export async function recordAndStop(page: Page) {
  await page.locator(".mic").click();
  await expect(page.locator(".mic-recording")).toBeVisible();
  await page.waitForTimeout(1200);
  await page.locator(".mic").click();
}

export const EN = {
  sttText: "How do I make water safe to drink?",
  sttLanguage: "en",
  answer: ["Boil it for one minute. ", "Then let it cool. ", "Store it clean."],
};

