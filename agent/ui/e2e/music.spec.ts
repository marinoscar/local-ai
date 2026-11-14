import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// E11 (issue #512): the /music/ page and the workload switcher, against e2e/fake-agent.mjs (its music routes switch
// and make a song by themselves a moment later and announce it on the event stream). Keyboard only where a person
// would use the keyboard; the CSP is the Portal's, so any inline style would fail the page.

async function setStatus(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__status", { data: patch });
}

async function setMusic(page: Page, patch: Record<string, unknown>) {
  await page.request.post("/__music", { data: patch });
}

async function seen(page: Page): Promise<{ path: string; type?: string; body?: string; grant?: string | null }[]> {
  return (await page.request.get("/__seen")).json();
}

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

test.beforeEach(async ({ page }) => {
  await page.request.post("/__reset");
  // Music runs outside survival, with the computer's own app (a full session).
  await setStatus(page, { mode: "normal", session: "full" });
});

test("not installed: says so with LAI-SVC-150 and the one thing to do", async ({ page }) => {
  await setMusic(page, { installed: false });
  await page.goto("/music/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Music");
  const notice = page.locator('[data-code="LAI-SVC-150"]');
  await expect(notice).toContainText("Music is not installed on this computer.");
  await expect(notice).toContainText("Install what is missing");
  await expect(notice).toContainText("Code: LAI-SVC-150");
  await expect(page.getByRole("button", { name: "Generate" })).toHaveCount(0);
});

test("survival mode: explains that music does not run there", async ({ page }) => {
  await setStatus(page, { mode: "survival", session: "portal" });
  await page.goto("/music/");
  await expect(page.locator('[data-code="LAI-LLM-053"]')).toContainText(
    "Music does not run in survival mode.",
  );
});

test("keyboard only: switch to Music, write a song with a sample, hear it, delete it", async ({ page }) => {
  await page.goto("/music/");
  await expect(page.getByText("Music is not loaded.", { exact: true })).toBeVisible();
  await expect(page.getByTestId("workload-now")).toHaveText("Now: Coding");
  await noSeriousViolations(page);

  // Switch to Music with the keyboard.
  const sw = page.getByRole("button", { name: "Switch to Music" });
  await sw.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByText("Switching to Music… This can take a few minutes.")).toBeVisible();
  await expect(page.locator(".music-ready")).toHaveText("Music is ready. Write your song below.", { timeout: 10_000 });
  expect((await seen(page)).some((s) => s.path === "/api/v1/workloads/music/activate")).toBe(true);

  // Lyrics with the section buttons, then the style.
  await page.getByRole("button", { name: "Add [verse]" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByLabel("Lyrics")).toBeFocused();
  await page.keyboard.type("Morning light on the hill");
  await page.getByLabel("Style").fill("acoustic guitar, calm");
  // Esc in the lyrics keeps the person on the page (it would otherwise go Home and lose the words).
  await page.getByLabel("Lyrics").press("Escape");
  await expect(page.getByLabel("Lyrics")).toHaveValue("[verse]\nMorning light on the hill");

  // A sample, used as "Same song, my lyrics".
  await page.getByLabel("Sample song (optional)").setInputFiles({
    name: "sample.wav",
    mimeType: "audio/wav",
    buffer: Buffer.from("RIFF0000WAVEfmt "),
  });
  await page.getByLabel("Same song, my lyrics").check();
  await noSeriousViolations(page);

  await page.getByRole("button", { name: "Generate" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByText(/Making your song… 50 %|Waiting for its turn…/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Cancel" })).toBeVisible();
  await expect(page.getByText("Your song is ready. It is in the list of songs.")).toBeVisible({ timeout: 10_000 });

  const gen = (await seen(page)).find((s) => s.path === "/api/v1/music/generate");
  expect(gen?.type).toContain("multipart/form-data");
  expect(gen?.body).toContain('name="sample_mode"\r\n\r\nsame_song');
  expect(gen?.body).toContain('name="strength"\r\n\r\n1');
  expect(gen?.body).toContain('filename="sample.wav"');

  // The song is in the list with a player and a download link.
  const title = page.getByRole("heading", { name: "Morning light on the hill" });
  await expect(title).toBeVisible();
  await expect(page.locator('audio[aria-label="Play Morning light on the hill"]')).toHaveAttribute(
    "src",
    "/api/v1/music/songs/song-1/audio",
  );
  await expect(page.getByRole("link", { name: "Download Morning light on the hill" })).toHaveAttribute(
    "download",
    "morning-light-on-the-hill.wav",
  );

  // Delete asks first, with "No, keep it" focused; Enter keeps it.
  await page.getByRole("button", { name: "Delete Morning light on the hill" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alertdialog")).toContainText("This cannot be undone.");
  await expect(page.getByRole("button", { name: "No, keep it" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(title).toBeVisible();

  // Yes deletes it.
  await page.getByRole("button", { name: "Delete Morning light on the hill" }).click();
  await page.getByRole("button", { name: "Yes, delete it" }).click();
  await expect(title).toHaveCount(0);
  await expect(page.getByText("No songs yet. Make one above.")).toBeVisible();
});

test("the admin PIN is asked inside the delete confirmation when the agent wants it", async ({ page }) => {
  await setMusic(page, {
    engine_state: "ready",
    loaded_model: "song-model",
    active: "music",
    deleteAnswer: 403,
    songs: [{ id: "s9", created_at: "2026-11-14T10:00:00Z", title: "Old song", tags: "", language: "en", duration_s: 30, model_id: "song-model", seed: 1, mime: "audio/wav", size_bytes: 10, origin: "local" }],
  });
  await page.goto("/music/");
  await page.getByRole("button", { name: "Delete Old song" }).click();
  await page.getByRole("button", { name: "Yes, delete it" }).click();
  await expect(page.locator('[data-code="LAI-SEC-002"]')).toBeVisible();
  await page.getByLabel("Deleting needs the admin PIN. Type it here:").fill("1111");
  await page.getByRole("button", { name: "Yes, delete it" }).click();
  await expect(page.locator('[data-code="LAI-SEC-003"]')).toContainText("That PIN is not right.");
  await page.getByLabel("Deleting needs the admin PIN. Type it here:").fill("4821");
  await page.getByRole("button", { name: "Yes, delete it" }).click();
  await expect(page.getByRole("heading", { name: "Old song" })).toHaveCount(0);
  const del = (await seen(page)).filter((s) => s.path === "/api/v1/music/songs/s9");
  expect(del.at(-1)?.grant).toBe("pg_fake");
});

test("a refused switch shows its doctor code and fix", async ({ page }) => {
  await setMusic(page, { activateAnswer: 422 });
  await page.goto("/music/");
  await page.getByRole("button", { name: "Switch to Music" }).click();
  const alert = page.locator('[data-code="LAI-LLM-051"]');
  await expect(alert).toContainText("Use it on a computer with a bigger graphics card.");
  await expect(alert).toContainText("Code: LAI-LLM-051");
});

test("a full queue shows LAI-SVC-153; cancel ends a song with LAI-SVC-158", async ({ page }) => {
  await setMusic(page, { engine_state: "ready", loaded_model: "song-model", active: "music", generateAnswer: 429 });
  await page.goto("/music/");
  await page.getByLabel("Lyrics").fill("Hello");
  await page.getByRole("button", { name: "Generate" }).click();
  await expect(page.locator('[data-code="LAI-SVC-153"]')).toContainText("Too many songs are waiting.");

  await setMusic(page, { generateAnswer: 0 });
  await page.getByRole("button", { name: "Generate" }).click();
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.locator('[data-code="LAI-SVC-158"]')).toContainText("The song was cancelled.");
});

test("Spanish, and no horizontal scroll in a narrow window", async ({ page }) => {
  await setMusic(page, { engine_state: "ready", loaded_model: "song-model", active: "music" });
  await page.setViewportSize({ width: 640, height: 480 });
  await page.goto("/music/");
  await page.getByRole("button", { name: /Español/ }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Música");
  await expect(page.getByRole("heading", { name: "Hacer una canción" })).toBeVisible();
  await expect(page.locator(".music-ready")).toHaveText("La música está lista. Escriba su canción abajo.");
  await page.getByLabel("Canción de muestra (opcional)").setInputFiles({
    name: "muestra.mp3",
    mimeType: "audio/mpeg",
    buffer: Buffer.from("ID3"),
  });
  await expect(page.getByLabel("Que suene así")).toBeChecked();
  await expect(page.getByLabel("La misma canción, con mi letra")).toBeVisible();
  const { sw, cw } = await page.evaluate(() => ({
    sw: document.documentElement.scrollWidth,
    cw: document.documentElement.clientWidth,
  }));
  expect(sw).toBeLessThanOrEqual(cw);
  await noSeriousViolations(page);
});

test("the Switch brain screen shows the workloads and opens Music", async ({ page }) => {
  await page.goto("/portal/brain");
  await expect(page.getByTestId("workload-now")).toHaveText("Now: Coding");
  await expect(page.getByRole("button", { name: "Switch to Music" })).toBeVisible();
  await page.getByRole("button", { name: "Open Music" }).click();
  await expect(page).toHaveURL(/\/portal\/music$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Music");
});
