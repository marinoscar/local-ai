// Connected mode, the link part (docs/specs/desktop.md 4.8 and 4.9, section 9): the device flow against tests/fixtures/fake-cloud.ts,
// the stored token, the egress switch and the abort within 1 s when the node leaves `online`. Drives the real app.
//
//   unpackaged:  npm run build && xvfb-run -a npx playwright test tests/e2e/connected.spec.ts
//   packaged:    LOCALAI_E2E_EXECUTABLE=release/test-copy/ai-desktop (as in helpers.ts)
//
// DSK-11 adds the Lab part at the end: the node cards, the allowlisted proxy as the page sees it, the commands, the cached view
// with its banner when the fake cloud goes down, and the retry that stops while the tab is hidden.
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
import { STRINGS } from '../../src/renderer/lab/strings';

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

// ---------------------------------------------------------------------------------------------------------------------
// The Lab tab (DSK-11)
// ---------------------------------------------------------------------------------------------------------------------

const isLabPage = (url: string): boolean => url.startsWith('localai://app/lab/');
const labCacheFile = (r: Launched) => join(configDir(r), 'lab-cache.json');
const tabOf = (bar: number) => evaluateIn<string>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.tab)');
const clickIn = (page: number, testid: string) => evaluateIn(run!.app, page, `document.querySelector('[data-testid="${testid}"]').click()`);
const cardCount = "document.querySelectorAll('[data-testid^=\"node-card-\"]').length";
const ALLOWED_SEEN = ['GET /api/auth/me', 'GET /api/localai/nodes'];

/** Links to the fake cloud, then shows the Lab tab and waits for its three cards. */
async function connectLab(): Promise<{ bar: number; lab: number }> {
  const { bar } = await start();
  await answerConsent(run!.app, true);
  await openLinkPage();
  await pollIn(run!.app, linkPage!, text('link-code'));
  cloud!.approve();
  await pollIn(run!.app, linkPage!, `${text('link-message')}.startsWith('Connected')`, 20_000);
  await expect.poll(() => evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)'), { timeout: 10_000 }).toBe(true);
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('lab')");
  const lab = (await waitForContents(run!.app, isLabPage)).id;
  await pollIn(run!.app, lab, `${cardCount} === 3`, 20_000);
  return { bar, lab };
}

test('the Lab tab shows the node cards from the cloud API, who is linked, and opens a node in the browser', async () => {
  const { bar, lab } = await connectLab();
  expect(await tabOf(bar)).toBe('lab');
  expect(await evaluateIn<string>(run!.app, lab, text('lab-user'))).toBe('Connected as Person A');
  expect(await evaluateIn<string>(run!.app, lab, text('lab-summary'))).toBe('3 computers · 2 online · 2 alerts');
  const page = await evaluateIn<string>(run!.app, lab, 'document.body.innerText');
  for (const word of ['ai-n1', 'MS-S1 Max', 'ai-n2', 'MSI Stealth', 'ai-n3', 'HP ZBook', 'Healthy', 'Offline mode (expected)', '2 alerts, 1 critical']) expect(page).toContain(word);
  expect(await evaluateIn<string>(run!.app, lab, "document.querySelector('[data-testid=\"lab-banner\"]') ? 'banner' : 'none'")).toBe('none');

  // Only the allowlisted reads went out, with the PAT as a bearer; the page never saw the PAT.
  const issued = cloud!.issued!;
  const seen = [...new Set(cloud!.requests.filter((r) => r.bearer === issued.token).map((r) => `${r.method} ${r.path}`))].sort();
  expect(seen).toEqual(ALLOWED_SEEN);
  expect(await evaluateIn<string>(run!.app, lab, 'document.documentElement.outerHTML')).not.toContain(issued.token);

  // The page itself can make no network request: its session allows only localai:.
  const before = cloud!.requests.length;
  expect(await evaluateIn<string>(run!.app, lab, "fetch('https://ai.marin.cr/api/localai/nodes').then(() => 'reached', () => 'blocked')")).toBe('blocked');
  expect(cloud!.requests).toHaveLength(before);

  // "Open in browser" opens the node's page in the cloud, nothing else.
  await run!.app.evaluate(({ shell }) => {
    (globalThis as any).__opened = [];
    (shell as any).openExternal = async (url: string) => void (globalThis as any).__opened.push(url);
  });
  await clickIn(lab, 'open-ai-n2');
  await expect.poll(() => run!.app.evaluate(() => (globalThis as any).__opened as string[])).toEqual(['https://ai.marin.cr/lab/nodes/ai-n2']);
});

test('lab-cache.json is written after a good load: 0600, node cards only, no token, key or address', async () => {
  await connectLab();
  await expect.poll(() => stat(labCacheFile(run!)).then(() => true, () => false)).toBe(true);
  expect((await stat(labCacheFile(run!))).mode & 0o777).toBe(0o600);
  const raw = await readFile(labCacheFile(run!), 'utf8');
  const file = JSON.parse(raw) as { schema: number; savedAt: string; nodes: Array<{ nodeId: string }>; summary: Record<string, number> };
  expect(file.schema).toBe(1);
  expect(file.nodes.map((n) => n.nodeId)).toEqual(['ai-n1', 'ai-n2', 'ai-n3']);
  expect(Date.now() - Date.parse(file.savedAt)).toBeLessThan(60_000);
  expect(raw).not.toMatch(/\b(?:pat|nod|lai|lat|pg)_/);
  expect(raw).not.toMatch(/\b[0-9a-f]{64}\b/);
  expect(raw).not.toContain(cloud!.issued!.token);
  expect(raw).not.toMatch(/\b100\.\d+\.\d+\.\d+\b/);
});

test('Load queues a model.load and shows Waiting…, Running… and Done from the polling', async () => {
  const { lab } = await connectLab();
  cloud!.holdCommand = true;
  await clickIn(lab, 'models-ai-n2');
  await pollIn(run!.app, lab, "document.querySelector('[data-testid=\"load-ai-n2-qwen3.5-9b-q4km\"]')?.disabled === false");
  // A model the node does not have cannot be loaded; the loaded default offers Unload.
  expect(await evaluateIn<boolean>(run!.app, lab, "document.querySelector('[data-testid=\"load-ai-n2-big-model-q8\"]').disabled")).toBe(true);
  expect(await evaluateIn<boolean>(run!.app, lab, "!!document.querySelector('[data-testid=\"unload-ai-n2-qwen3.6-35b-a3b-q4km\"]')")).toBe(true);

  await clickIn(lab, 'load-ai-n2-qwen3.5-9b-q4km');
  await pollIn(run!.app, lab, `${text('command-ai-n2')} === ${JSON.stringify(STRINGS.waiting.en)}`);
  expect(cloud!.commands).toHaveLength(1);
  expect(cloud!.commands[0]).toMatchObject({ nodeId: 'ai-n2', type: 'localai.model.load', params: { model_id: 'qwen3.5-9b-q4km', unload_others: true } });
  // The card's buttons are off while its command is under way.
  expect(await evaluateIn<boolean>(run!.app, lab, "document.querySelector('[data-testid=\"refresh-ai-n2\"]').disabled")).toBe(true);

  cloud!.holdCommand = false;
  const seen = new Set<string>();
  const end = Date.now() + 15_000;
  while (!seen.has('Done') && Date.now() < end) {
    seen.add(await evaluateIn<string>(run!.app, lab, text('command-ai-n2')));
    await sleep(150);
  }
  expect([...seen]).toEqual(expect.arrayContaining([STRINGS.running.en, STRINGS.done.en]));
  // Done: the card is usable again.
  await pollIn(run!.app, lab, "document.querySelector('[data-testid=\"refresh-ai-n2\"]').disabled === false");
  const polls = cloud!.requests.filter((r) => r.method === 'GET' && r.path === `/api/localai/commands/${cloud!.commands[0]!.id}`);
  expect(polls.length).toBeGreaterThanOrEqual(2);
  expect(cloud!.requests.filter((r) => r.method === 'POST' && r.path.startsWith('/api/localai/')).every((r) => r.path === '/api/localai/nodes/ai-n2/commands')).toBe(true);
});

test('the other buttons send their command types, and a restart asks first', async () => {
  const { lab } = await connectLab();
  await clickIn(lab, 'refresh-ai-n1');
  await pollIn(run!.app, lab, `${text('command-ai-n1')} === ${JSON.stringify(STRINGS.done.en)}`, 20_000);
  await clickIn(lab, 'sync-ai-n1');
  await pollIn(run!.app, lab, `${text('command-ai-n1')} === ${JSON.stringify(STRINGS.done.en)} && ${cardCount} === 3`, 20_000);
  await clickIn(lab, 'verify-ai-n1');
  await pollIn(run!.app, lab, "document.querySelector('[data-testid=\"verify-ai-n1\"]').disabled === false", 20_000);
  expect(cloud!.commands.map((c) => c.type)).toEqual(['localai.status.refresh', 'localai.ark.sync', 'localai.ark.verify']);

  // Restart service: pick one, confirm, and only then is anything sent.
  await clickIn(lab, 'restart-ai-n1');
  await pollIn(run!.app, lab, "!!document.querySelector('[data-testid=\"restart-ai-n1-whisper\"]')");
  await clickIn(lab, 'restart-ai-n1-whisper');
  await pollIn(run!.app, lab, "!!document.querySelector('[data-testid=\"restart-confirm\"]')");
  expect(cloud!.commands).toHaveLength(3);
  await clickIn(lab, 'restart-confirm');
  await expect.poll(() => cloud!.commands.length).toBe(4);
  expect(cloud!.commands[3]).toMatchObject({ nodeId: 'ai-n1', type: 'localai.service.restart', params: { service: 'whisper' } });
});

test('a refused command is told in plain words with its code', async () => {
  const { lab } = await connectLab();
  const cases: Array<[{ status: number; body: unknown }, string]> = [
    [{ status: 422, body: { statusCode: 422, message: 'This computer is in Offline mode. Only someone at it can switch it back.', details: { code: 'LAI-CLD-034', reason: 'node_offline_mode', mode: 'offline' } } }, STRINGS.refusedOffline.en],
    [{ status: 409, body: { statusCode: 409, message: 'This computer has been retired.', details: { code: 'LAI-CLD-035', reason: 'node_retired' } } }, STRINGS.refusedNotEnrolled.en],
    [{ status: 403, body: { statusCode: 403, message: 'You are not allowed to send this command', details: { reason: 'missing_permission' } } }, "Your lab role can't do this."],
  ];
  for (const [refusal, words] of cases) {
    cloud!.refuseCommands = refusal;
    await clickIn(lab, 'refresh-ai-n2');
    await pollIn(run!.app, lab, `${text('command-ai-n2')} === ${JSON.stringify(words)}`);
    // A refusal is not "busy": the button works again at once.
    expect(await evaluateIn<boolean>(run!.app, lab, "document.querySelector('[data-testid=\"refresh-ai-n2\"]').disabled")).toBe(false);
  }
  expect(cloud!.commands).toHaveLength(0);
});

test('the page can only make allowlisted calls: everything else is refused with no request to the cloud', async () => {
  const { bar, lab } = await connectLab();
  const before = cloud!.requests.length;
  const ask = (req: unknown) => evaluateIn<{ ok: boolean; error?: string }>(run!.app, lab, `window.localaiDesktop.cloud.request(${JSON.stringify(req)})`);
  const bad: unknown[] = [
    { method: 'GET', path: '/api/localai/../users' },
    { method: 'GET', path: '/api/localai/nodes/AI-N3' },
    { method: 'GET', path: '/api/users' },
    { method: 'GET', path: 'https://evil.example/api/localai/nodes' },
    { method: 'DELETE', path: `/api/localai/commands/${'0'.repeat(8)}-0000-0000-0000-${'0'.repeat(12)}` },
    { method: 'POST', path: '/api/localai/nodes/ai-n1/commands', body: { type: 'model.delete', params: {} } },
    { method: 'POST', path: '/api/localai/nodes/ai-n1/commands', body: { type: 'key.rotate' } },
    { method: 'GET', path: '/api/localai/nodes', headers: { authorization: 'Bearer x' } },
    { method: 'GET', path: '/api/localai/nodes?reveal=tailnet' },
  ];
  for (const req of bad) expect(await ask(req), JSON.stringify(req)).toMatchObject({ ok: false, error: 'refused' });
  expect(cloud!.requests).toHaveLength(before);
  // Another shell page (the top bar) may not call the proxy at all.
  const refusedFromBar = await evaluateIn<string>(run!.app, bar, "window.localaiDesktop.cloud.request({ method: 'GET', path: '/api/localai/nodes' }).then(() => 'answered', () => 'rejected')");
  expect(refusedFromBar).toBe('rejected');
  expect(cloud!.requests).toHaveLength(before);
});

test('cloud down: the cached cards with the banner, command buttons off; the retry stops while the tab is hidden and resumes when it is shown', async () => {
  test.setTimeout(120_000);
  const { bar, lab } = await connectLab();
  await expect.poll(() => stat(labCacheFile(run!)).then(() => true, () => false)).toBe(true);
  const savedAt = (JSON.parse(await readFile(labCacheFile(run!), 'utf8')) as { savedAt: string }).savedAt;

  // The cloud goes away; leaving the tab and coming back makes the page ask again at once.
  cloud!.down = true;
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('local')");
  await expect.poll(() => tabOf(bar)).toBe('local');
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('lab')");
  await pollIn(run!.app, lab, `${text('lab-banner-text')}.startsWith("Can't reach the lab right now.")`, 20_000);
  const time = new Intl.DateTimeFormat('en', { hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(savedAt));
  expect(await evaluateIn<string>(run!.app, lab, text('lab-banner-text'))).toBe(`Can't reach the lab right now. Showing information from ${time}. This computer keeps working normally.`);
  expect(await evaluateIn<string>(run!.app, lab, text('lab-banner'))).toContain('LAI-DSK-010');
  // The cards are the cached ones, the command buttons are off, "Open in browser" and local features are not affected.
  expect(await evaluateIn<number>(run!.app, lab, cardCount)).toBe(3);
  for (const id of ['refresh-ai-n1', 'sync-ai-n2', 'verify-ai-n3', 'models-ai-n2', 'restart-ai-n1']) expect(await evaluateIn<boolean>(run!.app, lab, `document.querySelector('[data-testid="${id}"]').disabled`), id).toBe(true);
  expect(await evaluateIn<boolean>(run!.app, lab, "document.querySelector('[data-testid=\"open-ai-n2\"]').disabled")).toBe(false);
  expect(await evaluateIn<string>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.agent)')).toBe('up');

  // Hidden: the retry (10 s after the failure) does not happen.
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('local')");
  await expect.poll(() => tabOf(bar)).toBe('local');
  const connections = cloud!.connections();
  await sleep(13_000);
  expect(cloud!.connections()).toBe(connections);

  // Shown again: it tries at once, and still fails.
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('lab')");
  await expect.poll(() => cloud!.connections(), { timeout: 10_000 }).toBeGreaterThan(connections);

  // The cloud is back: "Try now" (or the retry) clears the banner and the buttons work again.
  cloud!.down = false;
  await pollIn(run!.app, lab, "!!document.querySelector('[data-testid=\"lab-retry\"]')");
  await clickIn(lab, 'lab-retry');
  await pollIn(run!.app, lab, "!document.querySelector('[data-testid=\"lab-banner\"]') && document.querySelector('[data-testid=\"refresh-ai-n1\"]').disabled === false", 20_000);
});

test('a 5xx from the cloud is "unreachable" too: the cached view and the banner', async () => {
  const { bar, lab } = await connectLab();
  await expect.poll(() => stat(labCacheFile(run!)).then(() => true, () => false)).toBe(true);
  cloud!.nodesStatus = 503;
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('local')");
  await expect.poll(() => tabOf(bar)).toBe('local');
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('lab')");
  await pollIn(run!.app, lab, `${text('lab-banner-text')}.startsWith("Can't reach the lab right now.")`, 20_000);
  expect(await evaluateIn<number>(run!.app, lab, cardCount)).toBe(3);
});

test('leaving online takes the Lab tab away and the next request from the page is refused without a socket', async () => {
  const { bar, lab } = await connectLab();
  const before = { requests: cloud!.requests.length, connections: cloud!.connections() };
  agent!.emit('mode.step', { target: 'survival', step: 'first', state: 'started' });
  await expect.poll(() => tabOf(bar), { timeout: 5_000 }).toBe('local');
  expect(await evaluateIn<boolean>(run!.app, bar, 'window.localaiDesktop.status().then((s) => s.connected)')).toBe(false);
  const answer = await evaluateIn<{ ok: boolean; error?: string }>(run!.app, lab, "window.localaiDesktop.cloud.request({ method: 'GET', path: '/api/localai/nodes' })");
  expect(answer).toEqual({ ok: false, error: 'offline' });
  await evaluateIn(run!.app, bar, "window.localaiDesktop.showTab('lab')");
  expect(await tabOf(bar)).toBe('local'); // no Lab tab outside connected mode
  await sleep(300);
  expect(cloud!.requests).toHaveLength(before.requests);
  expect(cloud!.connections()).toBe(before.connections);
});

test('Disconnect deletes lab-cache.json', async () => {
  const { bar } = await connectLab();
  await expect.poll(() => stat(labCacheFile(run!)).then(() => true, () => false)).toBe(true);
  await evaluateIn(run!.app, bar, 'window.localaiDesktop.cloud.unlink()');
  await expect.poll(() => stat(labCacheFile(run!)).then(() => true, () => false)).toBe(false);
});
