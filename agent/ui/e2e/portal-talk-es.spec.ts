import { expect, test } from "@playwright/test";
import { micArgs, recordAndStop, rig, talk } from "./talk-helpers";

// PORTAL-07: Talk in Spanish with the fake microphone playing e2e/fixtures/es.wav.

test.use({ launchOptions: { args: micArgs("es.wav") }, permissions: ["microphone"] });

test.describe("Talk with the fake microphone (Spanish)", () => {

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

