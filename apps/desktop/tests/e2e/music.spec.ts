// Issue #515: the top bar's Music button and the Music window, against the fake agent with no internet at all.
// The button follows `GET /api/v1/music/status` (404 and `installed: false` say "not installed"); the window opens once
// at /music/, stays inside /music/, gets no microphone, and saves a song only through a "Save as" dialog.
import { expect, test } from '@playwright/test';
import { type FakeAgent, newFakeToken, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, childUrls, evaluateIn, isAgentUrl, isTopBar, launch, pollIn, waitForContents } from './helpers';

const MUSIC = 'http://127.0.0.1:7070/music/';
let agent: FakeAgent;
let run: Launched;

test.beforeAll(async () => {
  agent = await startFakeAgent({ port: 7070, token: newFakeToken() });
  // An agent from before E11: no music routes at all.
  agent.music = null;
  run = await launch({ token: agent.token });
  await waitForContents(run.app, isAgentUrl);
});

test.afterAll(async () => {
  await run?.stop();
  await agent?.close();
});

const STATE = "document.querySelector('[data-testid=open-music]')?.dataset.state";

test('an agent without the music routes (404): the button is there, disabled, and says why when pressed', async () => {
  const bar = await waitForContents(run.app, isTopBar);
  await pollIn(run.app, bar.id, `${STATE} === 'not-installed'`);
  expect(await evaluateIn<string>(run.app, bar.id, "document.querySelector('[data-testid=open-music]').getAttribute('aria-disabled')")).toBe('true');
  expect(await evaluateIn<string>(run.app, bar.id, "document.querySelector('[data-testid=open-music]').title")).toBe('Music is not installed on this computer');
  await evaluateIn(run.app, bar.id, "document.querySelector('[data-testid=open-music]').click()");
  await pollIn(run.app, bar.id, "document.querySelector('[data-testid=notice]').textContent === 'Music is not installed on this computer'");
  await new Promise((r) => setTimeout(r, 300));
  expect(await childUrls(run.app)).toEqual([]);
  expect(agent.requests.some((r) => r.path === '/api/v1/music/status')).toBe(true);
});

test('installed: false says the same; installed: true (after a sync finished) enables the button', async () => {
  const bar = await waitForContents(run.app, isTopBar);
  agent.music = { installed: false };
  agent.emit('sync.done', { run_id: 'r1', state: 'ok' });
  await new Promise((r) => setTimeout(r, 500));
  expect(await evaluateIn<string>(run.app, bar.id, STATE)).toBe('not-installed');
  agent.music = { installed: true };
  agent.emit('sync.done', { run_id: 'r2', state: 'ok' });
  await pollIn(run.app, bar.id, `${STATE} === 'installed'`);
  expect(await evaluateIn<string | null>(run.app, bar.id, "document.querySelector('[data-testid=open-music]').getAttribute('aria-disabled')")).toBeNull();
});

test('the Music button opens one window at /music/; a second press focuses it', async () => {
  const bar = await waitForContents(run.app, isTopBar);
  await evaluateIn(run.app, bar.id, "document.querySelector('[data-testid=open-music]').click()");
  const music = await waitForContents(run.app, (url) => url === MUSIC);
  await pollIn(run.app, music.id, "document.body.innerText.includes('/music/')");
  await evaluateIn(run.app, bar.id, "document.querySelector('[data-testid=open-music]').click()");
  await new Promise((r) => setTimeout(r, 500));
  expect(await childUrls(run.app)).toEqual([MUSIC]);
  // No preload: the agent's page needs no bridge.
  expect(await evaluateIn<string>(run.app, music.id, 'JSON.stringify([typeof window.localaiDesktop, typeof window.localaiDesktopHost])')).toBe('["undefined","undefined"]');
  // The sample picker is a plain file input: present and usable without any extra permission.
  expect(await evaluateIn<string>(run.app, music.id, "document.getElementById('sample').type")).toBe('file');
});

test('inside the Music window, leaving /music/ is blocked silently', async () => {
  const music = await waitForContents(run.app, (url) => url === MUSIC);
  for (const target of ['/chat/', '/', '/portal', 'https://example.com/']) {
    await evaluateIn(run.app, music.id, `location.href = ${JSON.stringify(target)}`);
    await new Promise((r) => setTimeout(r, 400));
    expect(await evaluateIn<string>(run.app, music.id, 'location.href')).toBe(MUSIC);
  }
  expect(await childUrls(run.app)).toEqual([MUSIC]);
});

test('the Music window gets no microphone', async () => {
  const music = await waitForContents(run.app, (url) => url === MUSIC);
  // The permission handler answers for this window; the page sees the microphone as not granted.
  const permission = await evaluateIn<string>(run.app, music.id, "navigator.permissions.query({ name: 'microphone' }).then((s) => s.state, () => 'error')");
  expect(permission).not.toBe('granted');
});

test('a song from the Music window asks where to save it (the "Save as" options), and nothing else is saved', async () => {
  const music = await waitForContents(run.app, (url) => url === MUSIC);
  // A second listener (after the app's) records what the app set and cancels before any dialog shows.
  await run.app.evaluate(({ session }) => {
    const g = globalThis as unknown as { __downloads: Array<Record<string, unknown>> };
    g.__downloads = [];
    session.fromPartition('persist:agent').on('will-download', (event, item) => {
      g.__downloads.push({ url: item.getURL(), prevented: event.defaultPrevented, options: item.getSaveDialogOptions(), savePath: item.getSavePath() });
      item.cancel();
    });
  });
  await evaluateIn(run.app, music.id, "document.getElementById('song').click()");
  await expect
    .poll(() => run.app.evaluate(() => (globalThis as unknown as { __downloads: unknown[] }).__downloads.length))
    .toBe(1);
  const [download] = await run.app.evaluate(() => (globalThis as unknown as { __downloads: Array<Record<string, unknown>> }).__downloads);
  expect(download).toMatchObject({ url: 'http://127.0.0.1:7070/api/v1/music/songs/sng_1/audio', prevented: false, savePath: '' });
  expect((download!['options'] as { title: string; defaultPath: string }).title).toBe('Save the song');
  expect((download!['options'] as { defaultPath: string }).defaultPath).toMatch(/\/song-1\.wav$/);
});
