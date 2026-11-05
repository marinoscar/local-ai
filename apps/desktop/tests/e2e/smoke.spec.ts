// Start-up against the fake agent (docs/specs/desktop.md 4.1, 9): the token from a file, the session in persist:agent,
// the dashboard, the event stream, the token staying in the main process, and the 401 recovery.
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { type FakeAgent, newFakeToken, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, childUrls, evaluateIn, isAgentUrl, isTopBar, launch, listContents, pollIn, waitForContents } from './helpers';

let agent: FakeAgent;
let run: Launched;
let token: string;
let agentPage: number;

test.beforeAll(async () => {
  token = newFakeToken();
  agent = await startFakeAgent({ port: 7070, token });
  run = await launch({ token });
  agentPage = (await waitForContents(run.app, isAgentUrl)).id;
});

test.afterAll(async () => {
  await run?.stop();
  await agent?.close();
});

test('POST /api/v1/session carries the token from the token file, and the dashboard shows the node', async () => {
  await pollIn(run.app, agentPage, "document.getElementById('node')?.textContent === 'node ai-n3 is online'");
  expect(agent.sessionRequests).toHaveLength(1);
  expect(agent.sessionRequests[0]).toEqual({ authorization: `Bearer ${token}`, accepted: true, origin: undefined });
  expect(await evaluateIn<string>(run.app, agentPage, "document.getElementById('dashboard').textContent")).toBe('Local AI dashboard');
});

test('the session cookie lives in persist:agent, HttpOnly', async () => {
  const cookies = await run.app.evaluate(({ session }) => session.fromPartition('persist:agent').cookies.get({ name: 'localai_session' }));
  expect(cookies).toHaveLength(1);
  expect(cookies[0]).toMatchObject({ httpOnly: true, sameSite: 'strict', domain: '127.0.0.1' });
});

test('the main process subscribed to /events with the bearer token', async () => {
  await expect.poll(() => agent.eventConnections.length).toBeGreaterThanOrEqual(1);
  expect(agent.eventConnections[0]).toEqual({ lastEventId: undefined, via: 'bearer' });
});

test('the top bar shows the status, the mode and the buttons; the Lab tab is hidden while not connected', async () => {
  const bar = await waitForContents(run.app, isTopBar);
  await pollIn(run.app, bar.id, "document.querySelector('[data-testid=pill]')?.dataset.tone === 'ok'");
  expect(await evaluateIn<string>(run.app, bar.id, "document.querySelector('[data-testid=pill]').getAttribute('aria-label')")).toBe('Ready');
  await pollIn(run.app, bar.id, "document.querySelector('[data-testid=mode]')?.textContent === 'Online'");
  const present = await evaluateIn<Record<string, boolean>>(
    run.app,
    bar.id,
    `Object.fromEntries(['tab-local', 'tab-lab', 'open-chat', 'open-library', 'open-maps', 'copy-key', 'menu'].map((id) => [id, document.querySelector('[data-testid=' + id + ']') !== null]))`,
  );
  expect(present).toEqual({ 'tab-local': true, 'tab-lab': false, 'open-chat': true, 'open-library': true, 'open-maps': true, 'copy-key': true, menu: true });
});

test('the Chat button opens one window at /chat/; a second click focuses it instead of opening another', async () => {
  const bar = await waitForContents(run.app, isTopBar);
  expect(await childUrls(run.app)).toEqual([]);
  await evaluateIn(run.app, bar.id, "document.querySelector('[data-testid=open-chat]').click()");
  const chat = await waitForContents(run.app, (url) => url === 'http://127.0.0.1:7070/chat/');
  await pollIn(run.app, chat.id, "document.body.innerText.includes('/chat/')");
  await evaluateIn(run.app, bar.id, "document.querySelector('[data-testid=open-chat]').click()");
  await new Promise((r) => setTimeout(r, 500));
  expect(await childUrls(run.app)).toEqual(['http://127.0.0.1:7070/chat/']);
  // It is a window of its own, not a view of the main one, and it carries no preload.
  const bridge = await evaluateIn<string>(run.app, chat.id, 'JSON.stringify([typeof window.localaiDesktop, typeof window.localaiDesktopHost])');
  expect(bridge).toBe('["undefined","undefined"]');
});

test('inside the Chat window, navigating to /library/ is blocked silently', async () => {
  const chat = await waitForContents(run.app, (url) => url === 'http://127.0.0.1:7070/chat/');
  await evaluateIn(run.app, chat.id, "location.href = '/library/'");
  await new Promise((r) => setTimeout(r, 700));
  expect(await childUrls(run.app)).toEqual(['http://127.0.0.1:7070/chat/']);
  expect(await evaluateIn<string>(run.app, chat.id, 'location.href')).toBe('http://127.0.0.1:7070/chat/');
});

test('a link from the agent UI to /library/ opens the Library window and leaves the agent UI where it was', async () => {
  await evaluateIn(run.app, agentPage, "location.href = '/library/'");
  await waitForContents(run.app, (url) => url === 'http://127.0.0.1:7070/library/');
  expect(await evaluateIn<string>(run.app, agentPage, 'location.href')).toBe('http://127.0.0.1:7070/');
});

test('a link to the internet from the agent UI is blocked and the top bar says so', async () => {
  const bar = await waitForContents(run.app, isTopBar);
  await evaluateIn(run.app, agentPage, "location.href = 'https://example.com/'");
  await pollIn(run.app, bar.id, "document.querySelector('[data-testid=notice]')?.textContent === 'Links to the internet are turned off on this computer.'");
  expect(await evaluateIn<string>(run.app, agentPage, 'location.href')).toBe('http://127.0.0.1:7070/');
});

test('the token is nowhere in any renderer: cookie, storage, window keys, DOM', async () => {
  const probe = `(() => {
    const dump = {
      cookie: document.cookie,
      local: JSON.stringify(Object.entries(localStorage)),
      session: JSON.stringify(Object.entries(sessionStorage)),
      keys: Object.getOwnPropertyNames(window).join(','),
      dom: document.documentElement.outerHTML,
      url: location.href,
    };
    return JSON.stringify(dump);
  })()`;
  // The window's own page has loaded nothing yet (no URL): there is no renderer state to look at.
  const pages = (await listContents(run.app)).filter((p) => p.url !== '');
  expect(pages.map((p) => p.url)).toContain('http://127.0.0.1:7070/');
  for (const page of pages) {
    const dump = await evaluateIn<string>(run.app, page.id, probe);
    expect(dump, 'the probe ran in this page').toContain(JSON.stringify(page.url).slice(1, -1));
    expect(dump, page.url).not.toContain(token);
    expect(dump, page.url).not.toContain('lat_');
  }
  // Nor in the log the main process keeps.
  const log = await readFile(join(run.configHome, 'ai-desktop', 'logs', 'main.log'), 'utf8').catch(() => '');
  expect(log).not.toContain(token);
  expect(run.stderr()).not.toContain(token);
});

test('a 401 after a token rotation: token re-read, new session, view reloaded; a second 401 within 30 s does not loop', async () => {
  const before = agent.sessionRequests.length;
  // The operator rotates the token and the agent forgets its sessions.
  token = newFakeToken();
  agent.token = token;
  agent.expireSessions();
  await (await import('node:fs/promises')).writeFile(run.tokenPath, `${token}\n`);

  const status = await evaluateIn<number>(run.app, agentPage, "fetch('/api/v1/status').then((r) => r.status)");
  expect(status).toBe(401);
  await expect.poll(() => agent.sessionRequests.length).toBe(before + 1);
  expect(agent.sessionRequests.at(-1)).toEqual({ authorization: `Bearer ${token}`, accepted: true, origin: undefined });
  // The reload makes the dashboard fetch /status again, now with the new cookie.
  await pollIn(run.app, agentPage, "document.getElementById('node')?.textContent === 'node ai-n3 is online'");

  agent.expireSessions();
  expect(await evaluateIn<number>(run.app, agentPage, "fetch('/api/v1/status').then((r) => r.status)")).toBe(401);
  await new Promise((r) => setTimeout(r, 1500));
  expect(agent.sessionRequests).toHaveLength(before + 1);
});
