import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// PORTAL-08: Ask about a photo (survival-portal.md sections 4.1.3 and 4.6) against the fake agent
// plus routed fakes for the chat and speech upstreams.

const here = dirname(fileURLToPath(import.meta.url));
const FIXTURE = join(here, "fixtures", "exif-orientation6-gps.jpg");

const VLM = {
  id: "vlm-1",
  display_name: "VLM One",
  roles: ["vision", "chat"],
  vision: true,
};
const CAPS_READY = {
  chat: { state: "ready" },
  photo: { state: "ready" },
  talk: { state: "ready" },
  library: { state: "ready" },
  maps: { state: "ready" },
  manuals: { state: "ready" },
};

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function withVision(page: Page) {
  await setStatus(page, {
    brain: { loaded: [VLM], loading: null, choices: [VLM] },
    capabilities: CAPS_READY,
  });
}

interface ChatBody {
  model: string;
  messages: { role: string; content: string | { type: string; image_url?: { url: string } }[] }[];
}

function sse(pieces: string[]): string {
  return (
    pieces
      .map((p) => `data: ${JSON.stringify({ choices: [{ delta: { content: p } }] })}\n\n`)
      .join("") + "data: [DONE]\n\n"
  );
}

async function rigChat(page: Page, pieces: string[]) {
  const bodies: ChatBody[] = [];
  await page.route("**/chat/v1/chat/completions", (route) => {
    bodies.push(route.request().postDataJSON() as ChatBody);
    return route.fulfill({ status: 200, contentType: "text/event-stream", body: sse(pieces) });
  });
  return bodies;
}

/** jpegInfo walks the segments of a JPEG up to the scan: its size and the markers it carries. */
function jpegInfo(buf: Buffer) {
  let w = 0;
  let h = 0;
  const markers: number[] = [];
  let i = 2;
  while (i + 4 <= buf.length && buf[i] === 0xff) {
    const m = buf[i + 1];
    markers.push(m);
    if (m === 0xda) break;
    const len = buf.readUInt16BE(i + 2);
    if (m >= 0xc0 && m <= 0xcf && m !== 0xc4 && m !== 0xc8 && m !== 0xcc) {
      h = buf.readUInt16BE(i + 5);
      w = buf.readUInt16BE(i + 7);
    }
    i += 2 + len;
  }
  return { w, h, markers, soi: buf[0] === 0xff && buf[1] === 0xd8 };
}

function silentWav(): Buffer {
  const n = 800;
  const b = Buffer.alloc(44 + n * 2);
  b.write("RIFF", 0);
  b.writeUInt32LE(36 + n * 2, 4);
  b.write("WAVEfmt ", 8);
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

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
});

async function choose(page: Page, file: string | { name: string; mimeType: string; buffer: Buffer }) {
  await page.getByTestId("choose-input").setInputFiles(file);
}

test("EXIF photo: upright, at most 1024 px, no EXIF or GPS in the request", async ({ page }) => {
  await withVision(page);
  const bodies = await rigChat(page, ["It looks like a ", "gradient."]);
  await page.goto("/portal/photo");
  await expect(page.locator("#main").getByText("Brain: VLM One")).toBeVisible();
  await choose(page, FIXTURE);
  await expect(page.getByRole("img", { name: "Your photo" })).toBeVisible();
  await page.getByLabel("Your question:").fill("What is this?");
  await page.getByLabel("Your question:").press("Enter");
  await expect(page.locator(".photo-answer .talk-answer")).toHaveText("It looks like a gradient.");

  expect(bodies).toHaveLength(1);
  const b = bodies[0];
  expect(b.model).toBe("vlm-1");
  expect(b.messages[0].content).toContain("Answer in English");
  const parts = b.messages[1].content as { type: string; image_url?: { url: string } }[];
  const url = parts.find((p) => p.type === "image_url")!.image_url!.url;
  expect(url.startsWith("data:image/jpeg;base64,")).toBe(true);
  const jpg = Buffer.from(url.split(",")[1], "base64");
  const info = jpegInfo(jpg);
  expect(info.soi).toBe(true);
  // Stored 1600x1000 with orientation 6: upright it is 1000x1600, so 640x1024.
  expect(info.w).toBe(640);
  expect(info.h).toBe(1024);
  expect(info.markers).not.toContain(0xe1); // no APP1: no EXIF, no GPS
  expect(jpg.includes(Buffer.from("Exif"))).toBe(false);
  expect(jpg.includes(Buffer.from("Fixture Camera"))).toBe(false);
});

test("a file over 30 MB and a file that is not an image are LAI-SVC-108", async ({ page }) => {
  await withVision(page);
  await page.goto("/portal/photo");
  await choose(page, { name: "big.jpg", mimeType: "image/jpeg", buffer: Buffer.alloc(31 * 1024 * 1024) });
  await expect(page.locator("#main").getByText("This photo can't be opened.")).toBeVisible();
  await expect(page.locator("#main").getByText("LAI-SVC-108")).toBeVisible();
  await choose(page, FIXTURE);
  await expect(page.getByRole("img", { name: "Your photo" })).toBeVisible();
  await choose(page, { name: "note.txt", mimeType: "text/plain", buffer: Buffer.from("hello") });
  await expect(page.locator("#main").getByText("This photo can't be opened.")).toBeVisible();
  await expect(page.getByRole("img", { name: "Your photo" })).toHaveCount(0);
});

test("needs_switch: Switch loads the vision model, then the question goes", async ({ page }) => {
  const plain = { id: "qwen3.5-9b-q4km", display_name: "Qwen 3.5 9B", roles: ["chat"], vision: false };
  await setStatus(page, {
    brain: { loaded: [plain], loading: null, choices: [plain, VLM] },
    capabilities: { ...CAPS_READY, photo: { state: "degraded", reason: "needs_switch", vision_model: "vlm-1" } },
  });
  const loads: string[] = [];
  await page.route("**/api/v1/models/vlm-1/load", async (route) => {
    loads.push(route.request().postData() ?? "");
    await page.request.post("/__status", {
      data: { brain: { loaded: [VLM], loading: null, choices: [plain, VLM] }, capabilities: CAPS_READY },
    });
    return route.fulfill({ status: 202, json: {} });
  });
  const bodies = await rigChat(page, ["A leaf."]);
  await page.goto("/portal/photo");
  await expect(
    page.locator("#main").getByText("The current brain can't see photos. Switch to VLM One? It takes about a minute."),
  ).toBeVisible();
  await choose(page, FIXTURE);
  await page.getByRole("button", { name: "Switch", exact: true }).click();
  await expect(page.locator("#main").getByText("Brain: VLM One")).toBeVisible({ timeout: 20000 });
  expect(loads).toHaveLength(1);
  await page.getByLabel("Your question:").fill("What is this?");
  await page.getByRole("button", { name: "Ask", exact: true }).click();
  await expect(page.locator(".photo-answer .talk-answer")).toHaveText("A leaf.");
  expect(bodies[0].model).toBe("vlm-1");
});

test("no vision model installed: the explanation screen says so", async ({ page }) => {
  await setStatus(page, {
    capabilities: { ...CAPS_READY, photo: { state: "unavailable", code: "LAI-SVC-040", reason: "no_vision_model" } },
  });
  await page.goto("/portal/photo");
  await expect(
    page.locator("#main").getByText("No brain that understands photos is installed on this computer."),
  ).toBeVisible();
});

test("Read aloud sends the answer to TTS one sentence at a time, in order", async ({ page }) => {
  await withVision(page);
  await rigChat(page, ["It is a plant. ", "Do not eat it. ", "Wash your hands."]);
  const tts: { text: string; language: string }[] = [];
  await page.route("**/api/v1/speech/tts", (route) => {
    tts.push(route.request().postDataJSON() as { text: string; language: string });
    return route.fulfill({ status: 200, contentType: "audio/wav", body: silentWav() });
  });
  await page.goto("/portal/photo");
  await choose(page, FIXTURE);
  await page.getByLabel("Your question:").fill("Is it safe?");
  await page.getByLabel("Your question:").press("Enter");
  await expect(page.locator(".photo-answer .talk-answer")).toContainText("Wash your hands.");
  await page.getByRole("button", { name: "Read aloud" }).click();
  await expect.poll(() => tts.map((x) => x.text)).toEqual(["It is a plant.", "Do not eat it.", "Wash your hands."]);
  expect(tts.every((x) => x.language === "en")).toBe(true);
});

test("follow-ups keep the image in the first message only and stop at four", async ({ page }) => {
  await withVision(page);
  const bodies = await rigChat(page, ["Answer."]);
  await page.goto("/portal/photo");
  await choose(page, FIXTURE);
  const box = page.getByLabel("Your question:");
  for (let i = 1; i <= 4; i++) {
    await box.fill(`Question ${i}`);
    await box.press("Enter");
    await expect.poll(() => bodies.length).toBe(i);
    await expect(page.getByRole("button", { name: "Read aloud" })).toBeEnabled();
  }
  const second = bodies[1].messages;
  expect(second.map((m) => m.role)).toEqual(["system", "user", "assistant", "user"]);
  expect(JSON.stringify(second[1])).toContain("image_url");
  expect(JSON.stringify(second[3])).not.toContain("image_url");
  expect(bodies.every((b) => b.messages.filter((m) => JSON.stringify(m).includes("image_url")).length === 1)).toBe(true);
  await expect(box).toBeDisabled();
  await expect(page.locator("#main").getByText("That is enough questions about this photo.")).toBeVisible();
  await page.getByRole("button", { name: "Ask another question" }).click();
  await expect(box).toBeEnabled();
});

test("Spanish UI: the Spanish prompt is used", async ({ page }) => {
  await withVision(page);
  const bodies = await rigChat(page, ["Una hoja."]);
  await page.goto("/portal/photo");
  await page.getByRole("button", { name: "Español", exact: true }).first().click();
  await choose(page, FIXTURE);
  await page.getByLabel("Su pregunta:").fill("¿Qué es esto?");
  await page.getByLabel("Su pregunta:").press("Enter");
  await expect(page.locator(".photo-answer .talk-answer")).toHaveText("Una hoja.");
  expect(bodies[0].messages[0].content).toContain("Responda en español");
});

test("keyboard only and axe", async ({ page }) => {
  await withVision(page);
  await page.goto("/portal/photo");
  await choose(page, FIXTURE);
  await expect(page.getByRole("img", { name: "Your photo" })).toBeVisible();
  const res = await new AxeBuilder({ page }).analyze();
  const bad = res.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
  // Tab reaches the buttons, the question box and the quick questions.
  await page.locator("#main").focus();
  const reached = new Set<string>();
  for (let i = 0; i < 20; i++) {
    await page.keyboard.press("Tab");
    reached.add(await page.evaluate(() => document.activeElement?.textContent?.trim() || document.activeElement?.id || ""));
  }
  for (const name of ["Take photo", "Choose file", "What is this?", "Read the text"]) {
    expect(reached.has(name), name).toBe(true);
  }
  expect(reached.has("photo-question")).toBe(true);
});
