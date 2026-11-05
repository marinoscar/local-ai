// Survival UX (docs/specs/desktop.md 4.7): the full-screen Portal, the 32 px strip, hide-not-quit, re-launch, `--portal`,
// and the Portal with an unreadable token file.
//
//   unpackaged:  npm run build && xvfb-run -a -s "-screen 0 1920x1080x24" npx playwright test tests/e2e/survival.spec.ts
//   packaged:    LOCALAI_E2E_EXECUTABLE=release/test-copy/ai-desktop (as in helpers.ts)
// A CI display has no window manager and no StatusNotifier watcher, so: the tray is forced with LOCALAI_DESKTOP_TRAY, and
// "tray click" and "re-launch" are driven the way the app receives them, as the `second-instance` event the single-instance
// lock delivers (a tray click calls the same `bringForward` as that event does; the tray's own wiring is in the unit tests).
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, evaluateIn, isAgentUrl, isTopBar, launch, pollIn, waitForContents } from './helpers';

let agent: FakeAgent | undefined;
let run: Launched | undefined;
test.afterEach(async () => {
  await run?.stop();
  await agent?.close();
  run = undefined;
  agent = undefined;
});

const TRAY = { LOCALAI_DESKTOP_TRAY: 'available' };

const mainWindow = <T>(fn: string): Promise<T> => run!.app.evaluate(({ BaseWindow }, code) => new Function('win', `return ${code}`)(BaseWindow.getAllWindows()[0]!) as unknown, fn) as Promise<T>;
const isFullScreen = (): Promise<boolean> => mainWindow<boolean>('win.isFullScreen()');
const isVisible = (): Promise<boolean> => mainWindow<boolean>('win.isVisible()');
const settingsLang = async (): Promise<string | undefined> => (JSON.parse(await readFile(join(run!.configHome, 'ai-desktop', 'settings.json'), 'utf8').catch(() => '{}')) as { lang?: string }).lang;
const agentPath = async (): Promise<string> => (await waitForContents(run!.app, isAgentUrl)).url.replace('http://127.0.0.1:7070', '');
const comeBack = (...argv: string[]): Promise<void> => run!.app.evaluate(({ app }, args) => void app.emit('second-instance', {}, ['ai-desktop', ...args], ''), argv);
const click = (bar: number, testId: string): Promise<unknown> => evaluateIn(run!.app, bar, `document.querySelector('[data-testid="${testId}"]').click()`);
const stripText = (bar: number): Promise<unknown> => evaluateIn(run!.app, bar, `document.querySelector('[data-testid="survival-strip"]')?.textContent ?? null`);

async function goSurvival(): Promise<number> {
  agent!.mode = 'survival';
  agent!.emit('mode.changed', { from: 'online', to: 'survival', op_id: 'op_e2e', warnings: [] });
  const bar = (await waitForContents(run!.app, isTopBar)).id;
  await expect.poll(isFullScreen, { timeout: 15_000 }).toBe(true);
  await expect.poll(agentPath, { timeout: 15_000 }).toBe('/portal');
  await pollIn(run!.app, bar, `!!document.querySelector('[data-testid="survival-strip"]')`);
  return bar;
}

test('mode.changed to survival: full-screen Portal with the strip; close hides; re-launch returns; leaving survival restores the dashboard', async () => {
  agent = await startFakeAgent({ port: 7070 });
  run = await launch({ token: agent.token, env: TRAY });
  await waitForContents(run.app, isAgentUrl);
  await expect.poll(() => agent!.openEventStreams()).toBe(1);
  expect(await isFullScreen()).toBe(false);
  const bar = (await waitForContents(run.app, isTopBar)).id;
  expect(await evaluateIn<number>(run.app, bar, 'window.innerHeight')).toBe(48);

  await goSurvival();
  // 32 px strip: "Survival mode", EN/ES, Exit full screen. No tabs, no Lab.
  expect(await evaluateIn<number>(run.app, bar, 'window.innerHeight')).toBe(32);
  expect(await stripText(bar)).toContain('Survival mode');
  expect(await stripText(bar)).toContain('Exit full screen');
  expect(await evaluateIn(run.app, bar, `document.querySelectorAll('[data-testid^="tab-"]').length`)).toBe(0);

  // Exit full screen only leaves full screen: the strip stays, the mode is untouched, the agent is never asked for a change.
  await click(bar, 'exit-fullscreen');
  await expect.poll(isFullScreen).toBe(false);
  await pollIn(run.app, bar, `document.querySelector('[data-testid="exit-fullscreen"]').textContent === 'Full screen'`);
  expect(await evaluateIn<number>(run.app, bar, 'window.innerHeight')).toBe(32);
  expect(agent.modeRequests).toHaveLength(0);
  expect(await agentPath()).toBe('/portal');

  // Close hides (a tray exists); the app and the agent connection keep running.
  await run.app.evaluate(({ BaseWindow }) => BaseWindow.getAllWindows()[0]!.close());
  await expect.poll(isVisible).toBe(false);
  expect(agent.openEventStreams()).toBe(1);

  // A tray click or a re-launch: the window is back, full screen, on the Portal.
  await comeBack();
  await expect.poll(isVisible).toBe(true);
  await expect.poll(isFullScreen).toBe(true);
  expect(await agentPath()).toBe('/portal');

  // The node leaves survival (the Portal's PIN, or the tray's): windowed dashboard, 48 px bar, no strip.
  agent.mode = 'online';
  agent.emit('mode.changed', { from: 'survival', to: 'online', op_id: 'op_e2e2', warnings: [] });
  await expect.poll(isFullScreen, { timeout: 15_000 }).toBe(false);
  await expect.poll(agentPath, { timeout: 15_000 }).toBe('/');
  await pollIn(run.app, bar, `!document.querySelector('[data-testid="survival-strip"]') && window.innerHeight === 48`);
  expect(await isVisible()).toBe(true);
  expect(agent.modeRequests).toHaveLength(0);
});

test('the EN/ES toggle sets settings.lang and the Portal language', async () => {
  agent = await startFakeAgent({ port: 7070 });
  run = await launch({ token: agent.token, env: TRAY });
  await waitForContents(run.app, isAgentUrl);
  await expect.poll(() => agent!.openEventStreams()).toBe(1);
  const bar = await goSurvival();

  await click(bar, 'lang-es');
  await expect.poll(settingsLang, { timeout: 15_000 }).toBe('es');
  await pollIn(run.app, bar, `document.querySelector('[data-testid="survival-label"]').textContent === 'Modo supervivencia'`);
  expect(await stripText(bar)).toContain('Salir de pantalla completa');
  // The Portal stores its language in localStorage and reads it at start-up, so it reloaded with the new value.
  const portal = (await waitForContents(run.app, isAgentUrl)).id;
  await expect.poll(() => evaluateIn(run!.app, portal, `localStorage.getItem('lai.portal.lang')`), { timeout: 15_000 }).toBe('es');

  await click(bar, 'lang-en');
  await expect.poll(settingsLang, { timeout: 15_000 }).toBe('en');
  await pollIn(run.app, bar, `document.querySelector('[data-testid="survival-label"]').textContent === 'Survival mode'`);
  await expect.poll(() => evaluateIn(run!.app, portal, `localStorage.getItem('lai.portal.lang')`), { timeout: 15_000 }).toBe('en');
});

test('a node that starts in survival mode opens the full-screen Portal at once', async () => {
  agent = await startFakeAgent({ port: 7070, mode: 'survival' });
  run = await launch({ token: agent.token, env: TRAY });
  const bar = (await waitForContents(run.app, isTopBar)).id;
  await expect.poll(agentPath, { timeout: 20_000 }).toBe('/portal');
  await expect.poll(isFullScreen, { timeout: 15_000 }).toBe(true);
  await pollIn(run.app, bar, `!!document.querySelector('[data-testid="survival-strip"]')`);
  expect(await isVisible()).toBe(true);
});

test('--portal on a second launch brings the first instance to the full-screen Portal; Back returns to the dashboard (no PIN, the mode is not touched)', async () => {
  agent = await startFakeAgent({ port: 7070 });
  run = await launch({ token: agent.token, env: TRAY });
  await waitForContents(run.app, isAgentUrl);
  await expect.poll(() => agent!.openEventStreams()).toBe(1);
  const bar = (await waitForContents(run.app, isTopBar)).id;
  expect(await isFullScreen()).toBe(false);

  await comeBack('--portal');
  await expect.poll(isFullScreen, { timeout: 15_000 }).toBe(true);
  await expect.poll(agentPath, { timeout: 15_000 }).toBe('/portal');
  await pollIn(run.app, bar, `!!document.querySelector('[data-testid="survival-strip"]')`);
  // Not survival mode: the strip says so and offers the way back.
  expect(await stripText(bar)).toContain('Survival Portal');
  await click(bar, 'survival-back');
  await expect.poll(isFullScreen, { timeout: 15_000 }).toBe(false);
  await expect.poll(agentPath, { timeout: 15_000 }).toBe('/');
  await pollIn(run.app, bar, `!document.querySelector('[data-testid="survival-strip"]')`);
  expect(agent.modeRequests).toHaveLength(0);
  expect(agent.mode).toBe('online');
});

test('survival mode with no usable token file: the Portal loads, the strip carries LAI-DSK-003 instead of the error page', async () => {
  agent = await startFakeAgent({ port: 7070, mode: 'survival' });
  run = await launch({ token: null, env: TRAY });
  const bar = (await waitForContents(run.app, isTopBar)).id;
  await expect.poll(agentPath, { timeout: 20_000 }).toBe('/portal');
  await expect.poll(isFullScreen, { timeout: 15_000 }).toBe(true);
  await pollIn(run.app, bar, `document.querySelector('[data-testid="survival-banner"]')?.textContent.includes('LAI-DSK-003')`);
  // No error page was ever shown, and no session was asked for.
  expect(agent.sessionRequests).toHaveLength(0);

  // The event stream needs the token, so the desktop watches /health: survival ended in the Portal, the start-up runs again.
  agent.mode = 'online';
  await expect.poll(isFullScreen, { timeout: 20_000 }).toBe(false);
  await waitForContents(run.app, (url) => url.startsWith('localai://app/error/') && url.includes('LAI-DSK-003'));
});
