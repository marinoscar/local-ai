// Connected mode, the link part (docs/specs/desktop.md 4.8 and 4.9, section 9): the device flow against tests/fixtures/fake-cloud.ts,
// the stored token, the egress switch and the abort within 1 s when the node leaves `online`. Drives the real app.
//
//   unpackaged:  npm run build && xvfb-run -a npx playwright test tests/e2e/connected.spec.ts
//   packaged:    LOCALAI_E2E_EXECUTABLE=release/test-copy/ai-desktop (as in helpers.ts)
//
// "Hosts mapping": the app connects `ai.marin.cr:443` to the fake cloud's loopback port in plain HTTP when it is started
// with LOCALAI_DESKTOP_TEST_CLOUD_PORT (an unpackaged run and the test copy only). Everything above the socket is the real
// code path: the URL, the egress decision, the bearer, the TLS-only rule for URLs.
import { readFile, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { type ElectronApplication, expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type FakeCloud, startFakeCloud } from '../fixtures/fake-cloud';
import { type Launched, evaluateIn, isAgentUrl, isTopBar, launch, openProbeWindow, pollIn, waitForContents } from './helpers';

let agent: FakeAgent | undefined;
let cloud: FakeCloud | undefined;
let run: Launched | undefined;
let linkPage: number | undefined;

test.afterEach(async () => {
  await run?.stop();
  await agent?.close();
  await cloud?.close();
  run = undefined;
  agent = undefined;
  cloud = undefined;
  linkPage = undefined;
});

const NO_TRAY = { LOCALAI_DESKTOP_TRAY: 'unavailable' };
const configDir = (r: Launched) => join(r.configHome, 'ai-desktop');
const tokenFile = (r: Launched) => join(configDir(r), 'cloud-token.bin');
const readSettings = async (r: Launched): Promise<Record<string, any>> => JSON.parse(await readFile(join(configDir(r), 'settings.json'), 'utf8').catch(() => '{}')) as Record<string, any>;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function start(options: { mode?: string; work?: string; keep?: boolean } = {}): Promise<{ bar: number }> {
  agent = await startFakeAgent({ port: 7070, ...(options.mode ? { mode: options.mode } : {}) });
  cloud ??= await startFakeCloud({ interval: 1 });
  run = await launch({ token: agent.token, env: { ...NO_TRAY, LOCALAI_DESKTOP_TEST_CLOUD_PORT: String(cloud.port) }, ...(options.work ? { work: options.work } : {}) });
  await waitForContents(run.app, isAgentUrl);
  const bar = (await waitForContents(run.app, isTopBar)).id;
  // Wait until the app knows the node's mode (the first status read).
  await pollIn(run.app, bar, "window.localaiDesktop.status().then((s) => s.mode !== null)");
  return { bar };
}

/** Answers the native "store it anyway?" question without a dialog; returns a counter of how often it was asked. */
async function answerConsent(app: ElectronApplication, yes: boolean): Promise<void> {
  await app.evaluate(({ dialog }, answer) => {
    (globalThis as any).__consentAsked = 0;
    (dialog as any).showMessageBox = async () => {
      (globalThis as any).__consentAsked += 1;
      return { response: answer ? 1 : 0 };
    };
  }, yes);
}
const consentAsked = (app: ElectronApplication) => app.evaluate(() => (globalThis as any).__consentAsked as number);

/** The link dialog as the menu opens it: the real page, with the shell preload, in a window of its own. */
async function openLinkPage(): Promise<number> {
  linkPage = await openProbeWindow(run!.app, { url: 'localai://app/link/', preload: 'shell' });
  return linkPage;
}
const text = (testid: string) => `(document.querySelector('[data-testid="${testid}"]')?.textContent ?? '')`;

test('the device flow against the fake cloud: code shown, approved elsewhere, the PAT stored 0600, linked, connected', async () => {
  const { bar } = await start();
  await answerConsent(run!.app, true);
  const page = await openLinkPage();

  // The code is the cloud's, shown large, with the instructions.
  await pollIn(run!.app, page, text('link-code'));
  expect(await evaluateIn<string>(run!.app, page, text('link-code'))).toBe(cloud!.userCodes[0]);
  expect(await evaluateIn<string>(run!.app, page, text('link-message'))).toContain('open ai.marin.cr/activate and enter this code');
  expect(cloud!.clientInfos).toHaveLength(1);
  expect(cloud!.clientInfos[0]).toMatchObject({ tokenType: 'pat', userAgent: expect.stringMatching(/^ai-desktop\//) });
  expect((cloud!.clientInfos[0] as { deviceName: string }).deviceName).toMatch(/^ai-desktop on .+/);

  // Not linked, and the cloud is open to the app only for this attempt.
  expect((await readSettings(run!)).cloud.linked).toBe(false);
  expect(await evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(false);

  // "Open the browser" opens the activation URL with the code, in the default browser, and nothing else.
  await run!.app.evaluate(({ shell }) => {
    (globalThis as any).__opened = [];
    (shell as any).openExternal = async (url: string) => void (globalThis as any).__opened.push(url);
  });
  await evaluateIn(run!.app, page, "document.querySelector('[data-testid=\"link-open\"]').click()");
  await expect.poll(() => run!.app.evaluate(() => (globalThis as any).__opened as string[])).toEqual([`https://ai.marin.cr/activate?code=${cloud!.userCodes[0]}`]);

  // The user approves on a phone; the next poll collects the PAT.
  cloud!.slowDownNext();
  cloud!.approve();
  await pollIn(run!.app, page, `${text('link-message')}.startsWith('Connected')`, 20_000);
  const issued = cloud!.issued!;
  expect(issued.token).toMatch(/^pat_/);

  // Stored: encrypted, 0600, settings say linked.
  expect((await stat(tokenFile(run!))).mode & 0o777).toBe(0o600);
  expect((await readFile(tokenFile(run!))).toString('latin1')).not.toContain(issued.token);
  expect((await readSettings(run!)).cloud.linked).toBe(true);
  expect(await consentAsked(run!.app)).toBeLessThanOrEqual(1); // asked only on a desktop with no keyring

  // Connected mode is active, who is linked is the display name (never the email).
  await expect.poll(() => evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(true);
  await expect.poll(() => evaluateIn<string>(run!.app, bar, "window.localaiDesktop.cloud.linkState().then((s) => s.user ?? '')")).toBe('Person A');
  const state = await evaluateIn<string>(run!.app, bar, 'window.localaiDesktop.cloud.linkState().then((s) => JSON.stringify(s))');
  expect(JSON.parse(state)).toMatchObject({ linked: true, tokenHeld: true, user: 'Person A' });
  expect(state).not.toContain(issued.token);
  expect(state).not.toContain('example.test');

  // The PAT only ever went to the cloud, as a bearer, and the device code only to the token route.
  expect(cloud!.requests.filter((r) => r.bearer !== undefined).map((r) => r.path)).toEqual(['/api/auth/me']);
  expect(cloud!.requests.filter((r) => r.bearer === issued.token)).toHaveLength(1);

  // Never in a renderer, a log line or the process output.
  for (const c of await run!.app.evaluate(({ webContents }) => webContents.getAllWebContents().map((w) => w.id))) {
    const dump = await evaluateIn<string>(run!.app, c, 'document.documentElement.outerHTML + JSON.stringify(Object.keys(window))').catch(() => '');
    expect(dump).not.toContain(issued.token);
  }
  expect(await readFile(join(configDir(run!), 'logs', 'main.log'), 'utf8')).not.toContain(issued.token);
  expect(run!.stderr()).not.toContain(issued.token);
});

test('a restart keeps the link: the token is read back and connected mode is active again', async () => {
  const { bar } = await start();
  await answerConsent(run!.app, true);
  await openLinkPage();
  await pollIn(run!.app, linkPage!, text('link-code'));
  cloud!.approve();
  await pollIn(run!.app, linkPage!, `${text('link-message')}.startsWith('Connected')`, 20_000);
  const issued = cloud!.issued!;
  const work = run!.work;
  await run!.stop({ keep: true });
  await agent!.close();

  agent = undefined;
  const second = await start({ work });
  await expect.poll(() => evaluateIn<boolean>(run!.app, second.bar, 'window.localaiDesktop.status().then((s) => s.connected)'), { timeout: 15_000 }).toBe(true);
  await expect.poll(() => cloud!.requests.some((r) => r.bearer === issued.token && r.path === '/api/auth/me')).toBe(true);
  void bar;
});

test('Disconnect: the PAT is revoked in the cloud, the file is deleted, linked is false', async () => {
  const { bar } = await start();
  await answerConsent(run!.app, true);
  await openLinkPage();
  await pollIn(run!.app, linkPage!, text('link-code'));
  cloud!.approve();
  await pollIn(run!.app, linkPage!, `${text('link-message')}.startsWith('Connected')`, 20_000);
  const issued = cloud!.issued!;

  await evaluateIn(run!.app, bar, 'window.localaiDesktop.cloud.unlink()');
  expect(cloud!.revoked).toEqual([issued.tokenId]);
  expect(cloud!.requests.find((r) => r.method === 'DELETE')?.bearer).toBe(issued.token);
  await expect(stat(tokenFile(run!))).rejects.toMatchObject({ code: 'ENOENT' });
  expect((await readSettings(run!)).cloud.linked).toBe(false);
  expect(await evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(false);
});

test('a denied code and an expired code show LAI-DSK-009 and "Try again" gives a new code', async () => {
  await start();
  const page = await openLinkPage();
  await pollIn(run!.app, page, text('link-code'));
  cloud!.deny();
  await pollIn(run!.app, page, `${text('link-message')}.includes('LAI-DSK-009')`, 20_000);
  expect(await evaluateIn<string>(run!.app, page, text('link-message'))).toContain('The code was refused');
  expect((await readSettings(run!)).cloud.linked).toBe(false);

  await evaluateIn(run!.app, page, "document.querySelector('[data-testid=\"link-retry\"]').click()");
  await pollIn(run!.app, page, `${text('link-code')} && ${text('link-code')} !== ${JSON.stringify(cloud!.userCodes[0])}`);
  expect(cloud!.userCodes).toHaveLength(2);
  cloud!.expire();
  await pollIn(run!.app, page, `${text('link-message')}.includes('The code expired. Try again.')`, 20_000);
  await expect(stat(tokenFile(run!))).rejects.toMatchObject({ code: 'ENOENT' });
});

test('linking is refused while the node is offline: "Switch this computer to Online first." and the cloud is not touched', async () => {
  await start({ mode: 'offline' });
  const page = await openLinkPage();
  await pollIn(run!.app, page, `${text('link-message')}.includes('Switch this computer to Online first.')`);
  expect(cloud!.requests).toHaveLength(0);
  expect(cloud!.connections()).toBe(0);
});

test('without consent on a desktop with no keyring the token stays in memory only (LAI-DSK-008)', async () => {
  const { bar } = await start();
  await answerConsent(run!.app, false);
  await openLinkPage();
  await pollIn(run!.app, linkPage!, text('link-code'));
  cloud!.approve();
  await pollIn(run!.app, linkPage!, `${text('link-message')}.startsWith('Connected')`, 20_000);
  if ((await consentAsked(run!.app)) === 0) {
    // A real keyring protects the key: nothing to refuse, the file exists.
    expect((await stat(tokenFile(run!))).mode & 0o777).toBe(0o600);
    return;
  }
  await expect(stat(tokenFile(run!))).rejects.toMatchObject({ code: 'ENOENT' });
  expect(await evaluateIn<string>(run!.app, linkPage!, text('link-detail'))).toContain('LAI-DSK-008');
  expect((await readSettings(run!)).secrets.allowPlaintext).toBe(false);
  // Still connected for this run.
  await expect.poll(() => evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(true);
});

test('leaving online aborts a cloud request in flight in under 1 s and the next one is refused without a socket', async () => {
  const { bar } = await start();
  await answerConsent(run!.app, true);
  await openLinkPage();
  await pollIn(run!.app, linkPage!, text('link-code'));
  // Hold the "who is linked" call that follows the link: it is the request in flight.
  cloud!.holdMe = true;
  cloud!.approve();
  await pollIn(run!.app, linkPage!, `${text('link-message')}.startsWith('Connected')`, 20_000);
  await expect.poll(() => cloud!.openRequests(), { timeout: 10_000 }).toBe(1);
  await expect.poll(() => evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(true);
  await expect.poll(() => agent!.openEventStreams()).toBe(1);

  // The agent starts its transition to offline: mode.step is the first thing the desktop hears, before mode.changed.
  const before = { requests: cloud!.requests.length, connections: cloud!.connections() };
  const sent = Date.now();
  agent!.emit('mode.step', { target: 'offline', step: 'first', state: 'started' });
  await expect.poll(() => cloud!.abortedAt.length, { timeout: 5_000 }).toBe(1);
  expect(cloud!.abortedAt[0]! - sent).toBeLessThan(1000);
  expect(cloud!.openRequests()).toBe(0);
  // Egress is closed already: the top bar's Lab gate is down although the agent has not committed anything.
  expect(await evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(false);

  // A request to the cloud from the main process is refused by the guard with no socket: nothing reaches the fake.
  const blocked = await run!.app.evaluate(async () => {
    try {
      await fetch('https://ai.marin.cr/api/localai/nodes');
      return 'reached';
    } catch (error) {
      return (error as Error).name;
    }
  });
  expect(blocked).toBe('EgressBlockedError');
  await sleep(300);
  expect(cloud!.requests).toHaveLength(before.requests);
  expect(cloud!.connections(), `connections at ${JSON.stringify(cloud!.connectionTimes.map((t) => t - sent))} ms from the mode.step; requests ${JSON.stringify(cloud!.requests.map((r) => [r.method, r.path, r.at - sent]))}`).toBe(before.connections);

  // The link survives the mode change: back online, connected mode is active again.
  cloud!.holdMe = false;
  agent!.mode = 'offline';
  agent!.emit('mode.changed', { from: 'online', to: 'offline', op_id: 'op_fake', warnings: [] });
  await sleep(300);
  expect(await evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(false);
  agent!.mode = 'online';
  agent!.emit('mode.changed', { from: 'offline', to: 'online', op_id: 'op_fake', warnings: [] });
  await expect.poll(() => evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)'), { timeout: 10_000 }).toBe(true);
  expect((await readSettings(run!)).cloud.linked).toBe(true);
});

test('a 401 from the cloud deletes the token and unlinks (LAI-DSK-011)', async () => {
  const { bar } = await start();
  await answerConsent(run!.app, true);
  await openLinkPage();
  await pollIn(run!.app, linkPage!, text('link-code'));
  cloud!.approve();
  await pollIn(run!.app, linkPage!, `${text('link-message')}.startsWith('Connected')`, 20_000);
  await expect.poll(() => evaluateIn<string>(run!.app, bar, "window.localaiDesktop.cloud.linkState().then((s) => s.user ?? '')")).toBe('Person A');
  const work = run!.work;
  await run!.stop({ keep: true });
  await agent!.close();
  agent = undefined;

  // The PAT is revoked cloud-side while the app is closed: the start-up "who is linked" call gets a 401.
  cloud!.rejectBearer = true;
  const second = await start({ work });
  await expect.poll(async () => (await readSettings(run!)).cloud.linked, { timeout: 15_000 }).toBe(false);
  await expect(stat(tokenFile(run!))).rejects.toMatchObject({ code: 'ENOENT' });
  expect(await evaluateIn<boolean>(run!.app, second.bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(false);
  expect(await readFile(join(configDir(run!), 'logs', 'main.log'), 'utf8')).toContain('"code":"LAI-DSK-011"');
});
