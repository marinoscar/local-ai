// Copy API key, the connection-info window and the native PIN prompt (docs/specs/desktop.md 4.6) in the real app,
// against the fake agent: the clipboard and its timed clear, the masked key and Reveal (the agent counts every read),
// the PIN window (own strict preload, wrong PIN, lock-out, no PIN set), exactly one rotate request with the grant, and
// no PIN, grant or key in any log. The clipboard timeout is shortened with LOCALAI_DESKTOP_CLIPBOARD_CLEAR_MS (honoured
// only by an unpackaged run and the test copy); the connection-info window is opened with LOCALAI_DESKTOP_TEST_OPEN.
import { randomInt } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, evaluateIn, isAgentUrl, isConnectionInfo, isPinWindow, isTopBar, launch, waitForContents, waitForWindowPage } from './helpers';

const CLEAR_MS = 1500;
const NO_TRAY = { LOCALAI_DESKTOP_TRAY: 'unavailable', LOCALAI_DESKTOP_CLIPBOARD_CLEAR_MS: String(CLEAR_MS) };
const newPin = (): string => String(randomInt(10_000_000, 99_999_999));
const mask = (key: string): string => `${key.slice(0, 4)}…${key.slice(-4)}`;

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

const clipboardText = (r: Launched): Promise<string> => r.app.evaluate(({ clipboard }) => clipboard.readText());

test('Copy API key puts the agent’s key on the clipboard and clears it after the timeout', async () => {
  run = await launch({ token: agent.token, env: NO_TRAY });
  const bar = (await waitForContents(run.app, isTopBar)).id;
  await waitForContents(run.app, isAgentUrl);

  expect(await evaluateIn<unknown>(run.app, bar, 'window.localaiDesktop.copyApiKey()')).toEqual({ ok: true, clearsInS: 2 });
  expect(await clipboardText(run)).toBe(agent.apiKey);
  expect(agent.keyRequests).toEqual([{ authorization: `Bearer ${agent.token}` }]);

  await expect.poll(() => clipboardText(run!), { timeout: 10_000 }).toBe('');
});

test('what the user copied meanwhile is left alone', async () => {
  run = await launch({ token: agent.token, env: NO_TRAY });
  const bar = (await waitForContents(run.app, isTopBar)).id;
  await evaluateIn(run.app, bar, 'window.localaiDesktop.copyApiKey()');
  expect(await clipboardText(run)).toBe(agent.apiKey);
  await run.app.evaluate(({ clipboard }) => clipboard.writeText('something else'));
  await new Promise((r) => setTimeout(r, CLEAR_MS + 1500));
  expect(await clipboardText(run)).toBe('something else');
});

test('the top bar button says it was copied; a key the agent will not give says why, with the agent’s code', async () => {
  run = await launch({ token: agent.token, env: NO_TRAY });
  const bar = (await waitForContents(run.app, isTopBar)).id;
  await evaluateIn(run.app, bar, "document.querySelector('[data-testid=copy-key]').click()");
  await expect.poll(() => evaluateIn<string>(run!.app, bar, "document.querySelector('[data-testid=notice]').textContent"), { timeout: 10_000 }).toMatch(/copied|copiada/i);

  agent.unauthorizedAll = true;
  expect(await evaluateIn<unknown>(run.app, bar, 'window.localaiDesktop.copyApiKey()')).toMatchObject({ ok: false, code: 'LAI-SEC-001' });
});

test.describe('the connection-info window and the PIN prompt', () => {
  test('masked key, Reveal re-reads GET /key, snippets get the key only after Reveal, Copy, then Rotate through the PIN window', async () => {
    const pin = newPin();
    // Same length, last digit changed: never equal to the right one.
    const wrong = `${pin.slice(0, 7)}${(Number(pin[7]) + 1) % 10}`;
    agent.pin = pin;
    const oldKey = agent.apiKey;
    run = await launch({ token: agent.token, env: { ...NO_TRAY, LOCALAI_DESKTOP_TEST_OPEN: 'connection-info' } });
    const page = await waitForWindowPage(run.app, isConnectionInfo);

    // The addresses, and the key masked: the page never has the full key until Reveal.
    await expect(page.getByTestId('url-local')).toHaveText('http://127.0.0.1:8080/v1');
    await expect(page.getByTestId('url-lab')).toHaveText('https://ai.marin.cr/v1');
    await expect(page.getByTestId('key')).toHaveText(mask(oldKey));
    expect(await page.content()).not.toContain(oldKey);
    expect(agent.keyRequests).toHaveLength(1);

    // curl tab: the placeholder, until Reveal.
    await page.getByTestId('tab-curl').click();
    await expect(page.getByTestId('snippet-curl')).toContainText('$KEY');
    await expect(page.getByTestId('snippet-curl-models')).toHaveText('curl http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $(cat ~/.config/opencode/llama-api-key)"');

    // Reveal asks the agent again (the fake counts two reads), and fills the snippets.
    await page.getByTestId('reveal').click();
    await expect(page.getByTestId('key')).toHaveText(oldKey);
    expect(agent.keyRequests).toHaveLength(2);
    await expect(page.getByTestId('snippet-curl')).toContainText(`Bearer ${oldKey}`);
    await page.getByTestId('reveal').click(); // Hide
    await expect(page.getByTestId('key')).toHaveText(mask(oldKey));
    await expect(page.getByTestId('snippet-curl')).toContainText('$KEY');

    // The other tabs: the OpenCode block reads the key from a file, Open WebUI shows the URL and the masked key.
    await page.getByTestId('tab-opencode').click();
    await expect(page.getByTestId('snippet-write')).toHaveText('install -m 600 /dev/stdin ~/.config/opencode/llama-api-key');
    await expect(page.getByTestId('snippet-opencode')).toContainText('{file:~/.config/opencode/llama-api-key}');
    await expect(page.getByTestId('snippet-opencode')).toContainText('"baseURL": "http://127.0.0.1:8080/v1"');
    await page.getByTestId('tab-webui').click();
    await expect(page.getByTestId('snippet-webui')).toContainText(mask(oldKey));

    // Copy goes through the main process (and its timed clear).
    await page.getByTestId('copy').click();
    await expect(page.getByTestId('copied')).toContainText('2');
    expect(await clipboardText(run)).toBe(oldKey);

    // Rotate: the PIN window opens, with its own strict bridge and not the shell one.
    await page.getByTestId('rotate').click();
    const pinPage = await waitForWindowPage(run.app, isPinWindow);
    expect(await pinPage.evaluate('JSON.stringify({ shell: typeof window.localaiDesktop, host: typeof window.localaiDesktopHost, pin: Object.keys(window.localaiPin).sort() })')).toBe(
      JSON.stringify({ shell: 'undefined', host: 'undefined', pin: ['cancel', 'submit'] }),
    );
    const input = pinPage.getByTestId('pin-input');
    await expect(input).toHaveAttribute('type', 'password');
    // Digits only, 8 at most; OK stays disabled below 4.
    await input.pressSequentially('12a');
    await expect(input).toHaveValue('12');
    await expect(pinPage.getByTestId('pin-ok')).toBeDisabled();
    await input.fill('');

    // A wrong PIN: the remaining tries, the code, and the field is emptied. Nothing was rotated.
    await input.pressSequentially(wrong);
    await pinPage.getByTestId('pin-ok').click();
    await expect(pinPage.getByTestId('pin-feedback')).toContainText('tries left');
    await expect(pinPage.getByTestId('pin-code')).toHaveText('LAI-SEC-003');
    await expect(input).toHaveValue('');
    expect(agent.keyRotations).toHaveLength(0);

    // The right PIN: the window closes, and exactly one rotate request carries the grant.
    await input.pressSequentially(pin);
    await pinPage.getByTestId('pin-ok').click();
    await expect(page.getByTestId('rotated')).toBeVisible({ timeout: 20_000 });
    expect(agent.keyRotations).toHaveLength(1);
    expect(agent.keyRotations[0]).toMatchObject({ body: { restart: true }, authorization: `Bearer ${agent.token}`, accepted: true });
    expect(agent.keyRotations[0]!.pinGrant).toMatch(/^pg_[A-Za-z0-9_-]{43}$/);
    expect(agent.apiKey).not.toBe(oldKey);
    await expect(page.getByTestId('rotated')).toContainText(mask(agent.apiKey));
    await expect(page.getByTestId('clients')).toContainText('~/.config/opencode/llama-api-key');
    await expect(page.getByTestId('key')).toHaveText(mask(agent.apiKey));
    await expect.poll(() => run!.app.windows().filter((p) => !p.isClosed() && isPinWindow(p.url()))).toHaveLength(0);

    // No PIN, grant or key in any log (stderr and main.log).
    const grant = agent.keyRotations[0]!.pinGrant!;
    const logs = run.stderr() + (await readFile(join(run.configHome, 'ai-desktop', 'logs', 'main.log'), 'utf8'));
    for (const secret of [pin, wrong, grant, oldKey, agent.apiKey]) expect(logs).not.toContain(secret);
  });

  test('too many tries shows the wait message and its code; no PIN set shows sudo aictl pin set; closing rotates nothing', async () => {
    agent.pin = newPin();
    run = await launch({ token: agent.token, env: { ...NO_TRAY, LOCALAI_DESKTOP_TEST_OPEN: 'connection-info' } });
    const page = await waitForWindowPage(run.app, isConnectionInfo);
    await expect(page.getByTestId('key')).toBeVisible();

    agent.pinFailures = 5;
    await page.getByTestId('rotate').click();
    let pinPage = await waitForWindowPage(run.app, isPinWindow);
    await pinPage.getByTestId('pin-input').pressSequentially(newPin());
    await pinPage.getByTestId('pin-ok').click();
    await expect(pinPage.getByTestId('pin-feedback')).toContainText('Too many tries. Wait 5 minutes.');
    await expect(pinPage.getByTestId('pin-code')).toHaveText('LAI-SEC-004');
    await expect(pinPage.getByTestId('pin-input')).toBeDisabled();
    await pinPage.getByTestId('pin-cancel').click();
    await expect(page.getByTestId('rotate')).toBeEnabled();

    agent.pin = null;
    await page.getByTestId('rotate').click();
    pinPage = await waitForWindowPage(run.app, isPinWindow);
    await pinPage.getByTestId('pin-input').pressSequentially(newPin());
    await pinPage.getByTestId('pin-ok').click();
    await expect(pinPage.getByTestId('pin-feedback')).toContainText('sudo aictl pin set');
    await expect(pinPage.getByTestId('pin-code')).toHaveText('LAI-SEC-005');
    await pinPage.getByTestId('pin-cancel').click();

    expect(agent.keyRotations).toHaveLength(0);
    expect(agent.pinVerifications.map((v) => v.outcome)).toEqual(['locked', 'no-pin']);
  });

  test('leaving survival from the shell bridge needs no PIN (ADR-0011): one request, no PIN window', async () => {
    agent.mode = 'survival';
    run = await launch({ token: agent.token, env: NO_TRAY });
    const bar = (await waitForContents(run.app, isTopBar)).id;
    await waitForContents(run.app, isAgentUrl);
    await expect.poll(() => agent.openEventStreams()).toBe(1);

    expect(await evaluateIn<unknown>(run.app, bar, "window.localaiDesktop.setMode('online')")).toEqual({ ok: true });
    expect(agent.modeRequests).toHaveLength(1);
    expect(agent.modeRequests[0]).toMatchObject({ path: '/api/v1/mode', body: { target: 'online', wait: false }, pinGrant: undefined });
    expect(agent.pinVerifications).toHaveLength(0);
    expect(agent.mode).toBe('online');
  });

  test('an agent older than ADR-0011 still wants the PIN: the PIN window opens and the grant goes with the one retry', async () => {
    const pin = newPin();
    agent.pin = pin;
    agent.mode = 'survival';
    agent.leaveSurvivalNeedsPin = true;
    run = await launch({ token: agent.token, env: NO_TRAY });
    const bar = (await waitForContents(run.app, isTopBar)).id;
    await waitForContents(run.app, isAgentUrl);
    await expect.poll(() => agent.openEventStreams()).toBe(1);

    const leaving = evaluateIn<unknown>(run.app, bar, "window.localaiDesktop.setMode('online')");
    const pinPage = await waitForWindowPage(run.app, isPinWindow);
    await pinPage.getByTestId('pin-input').pressSequentially(pin);
    await pinPage.getByTestId('pin-ok').click();
    expect(await leaving).toEqual({ ok: true });
    const withGrant = agent.modeRequests.filter((r) => r.pinGrant !== undefined);
    expect(withGrant).toHaveLength(1);
    expect(withGrant[0]).toMatchObject({ path: '/api/v1/mode', body: { target: 'online', wait: false } });
    expect(agent.mode).toBe('online');
  });

  test('pinSubmit from the top bar or any other page is refused', async () => {
    run = await launch({ token: agent.token, env: NO_TRAY });
    const bar = (await waitForContents(run.app, isTopBar)).id;
    expect(await evaluateIn<string>(run.app, bar, "window.localaiDesktop.pinSubmit('1234').then(() => 'accepted', () => 'refused')")).toBe('refused');
    // The key, its reveal and the rotation are for the connection-info page only.
    for (const call of ['connectionInfo()', 'revealKey()', 'rotateKey()']) {
      expect(await evaluateIn<string>(run.app, bar, `window.localaiDesktop.${call}.then(() => 'accepted', () => 'refused')`), call).toBe('refused');
    }
    expect(agent.keyRequests).toHaveLength(0);
    expect(agent.pinVerifications).toHaveLength(0);
  });
});
