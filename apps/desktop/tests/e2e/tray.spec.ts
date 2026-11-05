// The tray's parts that need the real app (docs/specs/desktop.md 4.3, 5): the `setMode` bridge of the shell pages and
// the "no tray" fallback. A real tray needs a StatusNotifier watcher on a session bus, which a CI display does not
// have, so the tray's own behaviour (colours, menu, notifications, mode switch) is covered by the unit tests; this
// spec forces the no-tray path with LOCALAI_DESKTOP_TRAY=unavailable (honoured only by an unpackaged run and the test copy).
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { type FakeAgent, newFakeToken, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, evaluateIn, isAgentUrl, isPinWindow, launch, waitForContents, waitForWindowPage } from './helpers';

let agent: FakeAgent;
let run: Launched | undefined;

test.beforeEach(async () => {
  agent = await startFakeAgent({ port: 7070 });
});
test.afterEach(async () => {
  await run?.stop();
  run = undefined;
  await agent.close();
});

const NO_TRAY = { LOCALAI_DESKTOP_TRAY: 'unavailable' };

test('the shell bridge switches the mode through the agent; agent pages have no such bridge', async () => {
  run = await launch({ token: agent.token, env: NO_TRAY });
  const agentPage = await waitForContents(run.app, isAgentUrl);
  const shell = await waitForContents(run.app, (url) => url.startsWith('localai://app/topbar/'));

  expect(await evaluateIn<string>(run.app, agentPage.id, 'typeof window.localaiDesktop')).toBe('undefined');
  expect(await evaluateIn<unknown>(run.app, shell.id, "window.localaiDesktop.setMode('offline')")).toEqual({ ok: true });
  expect(agent.modeRequests).toHaveLength(1);
  expect(agent.modeRequests[0]).toMatchObject({ path: '/api/v1/mode', body: { target: 'offline', wait: false }, authorization: `Bearer ${agent.token}` });

  // Survival: no credential at all.
  expect(await evaluateIn<unknown>(run.app, shell.id, "window.localaiDesktop.setMode('survival')")).toEqual({ ok: true });
  expect(agent.modeRequests[1]).toMatchObject({ path: '/api/v1/mode/survival', body: {}, authorization: undefined });

  // A target the bridge does not know never reaches the agent.
  expect(await evaluateIn<unknown>(run.app, shell.id, "window.localaiDesktop.setMode('reboot')")).toEqual({ ok: false });
  // Leaving survival needs the PIN: the PIN window opens (DSK-08), and cancelling it asks the agent for nothing more.
  const before = agent.modeRequests.length;
  const leaving = evaluateIn<unknown>(run.app, shell.id, "window.localaiDesktop.setMode('online')");
  const pinPage = await waitForWindowPage(run.app, isPinWindow);
  await pinPage.getByTestId('pin-cancel').click();
  expect(await leaving).toEqual({ ok: false, code: 'LAI-SEC-002' });
  expect(agent.modeRequests.slice(before).every((r) => r.pinGrant === undefined)).toBe(true);
  expect(agent.mode).toBe('survival');
});

test('with no tray, LAI-DSK-013 is logged and remembered once, and closing the window quits the app', async () => {
  run = await launch({ token: agent.token, env: NO_TRAY });
  await waitForContents(run.app, isAgentUrl);
  const settingsFile = join(run.configHome, 'ai-desktop', 'settings.json');
  await expect
    .poll(async () => (JSON.parse(await readFile(settingsFile, 'utf8').catch(() => '{}')) as { notices?: Record<string, boolean> }).notices?.['LAI-DSK-013'], { timeout: 15_000 })
    .toBe(true);
  const log = await readFile(join(run.configHome, 'ai-desktop', 'logs', 'main.log'), 'utf8');
  expect(log).toContain('"code":"LAI-DSK-013"');

  const exited = new Promise<void>((resolve) => run!.app.process().once('exit', () => resolve()));
  await run.app.evaluate(({ BaseWindow }) => BaseWindow.getAllWindows()[0]!.close());
  await Promise.race([exited, new Promise((_r, reject) => setTimeout(() => reject(new Error('the app did not quit when its window was closed')), 15_000))]);
});

test('with a tray, closing the window hides it and the app keeps running; Quit really quits', async () => {
  run = await launch({ token: agent.token, env: { LOCALAI_DESKTOP_TRAY: 'available' } });
  await waitForContents(run.app, isAgentUrl);
  // The agent is up and the stream is open: the tray controller is wired to the feed.
  await expect.poll(() => agent.openEventStreams()).toBe(1);
  expect(await run.app.evaluate(({ BaseWindow }) => BaseWindow.getAllWindows()[0]!.isVisible())).toBe(true);

  let exited = false;
  run.app.process().once('exit', () => (exited = true));
  await run.app.evaluate(({ BaseWindow }) => BaseWindow.getAllWindows()[0]!.close());
  await expect.poll(() => run!.app.evaluate(({ BaseWindow }) => BaseWindow.getAllWindows().map((w) => w.isVisible()))).toEqual([false]);
  await new Promise((r) => setTimeout(r, 500));
  expect(exited).toBe(false);

  // Quit really quits (a quit in progress is never turned into "hide").
  const gone = new Promise<void>((resolve) => run!.app.process().once('exit', () => resolve()));
  await run.app.evaluate(({ app }) => app.quit());
  await Promise.race([gone, new Promise((_r, reject) => setTimeout(() => reject(new Error('Quit did not quit')), 15_000))]);
  expect(await readFile(join(run.configHome, 'ai-desktop', 'logs', 'main.log'), 'utf8')).not.toContain('"level":"error"');
});
