// "Install what's missing" (issue #480, docs/specs/desktop.md 4.12) in the real app, against the fake agent: the window opens
// state first, the button sends the safe default, progress and Stop, compiling or restarting a running service needs the
// confirmation dialog AND the administrator PIN (the grant rides in the header of that one request), and the top bar may
// not call it. The window is opened with LOCALAI_DESKTOP_TEST_OPEN (a native menu item cannot be clicked from Playwright).
import { randomInt } from 'node:crypto';
import { expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, evaluateIn, isPinWindow, isTopBar, launch, waitForContents, waitForWindowPage } from './helpers';

const NO_TRAY = { LOCALAI_DESKTOP_TRAY: 'unavailable' };
const isEnsure = (url: string): boolean => url.startsWith('localai://app/ensure/');
const newPin = (): string => String(randomInt(10_000_000, 99_999_999));

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

test('Needs you first, then the button sends the safe default; Stop cancels and what is left is shown', async () => {
  run = await launch({ token: agent.token, env: { ...NO_TRAY, LOCALAI_DESKTOP_TEST_OPEN: 'ensure' } });
  const page = await waitForWindowPage(run.app, isEnsure);
  await expect(page.getByTestId('state')).toContainText('Needs you: the voice program is missing.');
  await expect(page.getByTestId('next')).toContainText("Press Install what's missing.");
  await expect(page.getByTestId('step-tool.piper')).toContainText('Voice program (piper)');

  await page.getByTestId('install').click();
  await expect(page.getByTestId('stop')).toBeVisible();
  await expect(page.getByTestId('progress')).toBeVisible();
  const posts = agent.ensureRequests.filter((r) => r.method === 'POST');
  expect(posts).toHaveLength(1);
  expect(posts[0]).toMatchObject({ body: {}, authorization: `Bearer ${agent.token}`, language: 'en', pinGrant: undefined });

  await page.getByTestId('stop').click();
  await expect(page.getByTestId('note')).toContainText('Stopped. What was already installed stays.');
  expect(agent.ensureRequests.some((r) => r.method === 'DELETE')).toBe(true);
  await expect(page.getByTestId('step-tool.piper')).toHaveAttribute('data-status', 'cancelled');
});

test('a refusal keeps its code and the button stays usable', async () => {
  run = await launch({ token: agent.token, env: { ...NO_TRAY, LOCALAI_DESKTOP_TEST_OPEN: 'ensure' } });
  const page = await waitForWindowPage(run.app, isEnsure);
  agent.failNextEnsure = { status: 409, code: 'LAI-SYN-005', message: 'A copy or check is running', fix: 'Try again when it ends.' };
  await page.getByTestId('install').click();
  await expect(page.getByTestId('problem')).toContainText('A copy or check is running');
  await expect(page.getByTestId('problem-code')).toHaveText('LAI-SYN-005');
  await expect(page.getByTestId('install')).toBeEnabled();
});

test('compiling needs the confirmation dialog and the PIN; the grant goes with that one request', async () => {
  const pin = newPin();
  agent.pin = pin;
  agent.ensureView = {
    ...agent.ensureView,
    steps: [{ id: 'whisper.build', kind: 'build_whisper', status: 'needs_confirm', needs_confirmation: true, title: 'Build the speech program', detail: 'Compiles it from source.' }],
  };
  run = await launch({ token: agent.token, env: { ...NO_TRAY, LOCALAI_DESKTOP_TEST_OPEN: 'ensure' } });
  const page = await waitForWindowPage(run.app, isEnsure);
  // The main button is not offered for a step that only a person may approve.
  await expect(page.getByTestId('install')).toHaveCount(0);
  await page.getByTestId('approve').click();
  await expect(page.getByTestId('confirm')).toContainText('It can take an hour or more');
  await page.getByTestId('confirm-no').click();
  expect(agent.ensureRequests.filter((r) => r.method === 'POST')).toHaveLength(0);

  await page.getByTestId('approve').click();
  await page.getByTestId('confirm-yes').click();
  const pinPage = await waitForWindowPage(run.app, isPinWindow);
  await expect(pinPage.getByRole('heading')).toHaveText('Approve the install');
  await pinPage.getByTestId('pin-input').pressSequentially(pin);
  await pinPage.getByTestId('pin-ok').click();
  await expect(page.getByTestId('stop')).toBeVisible({ timeout: 20_000 });
  const posts = agent.ensureRequests.filter((r) => r.method === 'POST');
  expect(posts).toHaveLength(1);
  expect(posts[0]).toMatchObject({ body: { confirm: ['whisper.build'] }, accepted: true });
  expect(posts[0]!.pinGrant).toMatch(/^pg_[A-Za-z0-9_-]{43}$/);
});

test('closing the PIN window asks nothing of the agent', async () => {
  agent.pin = newPin();
  agent.ensureView = {
    ...agent.ensureView,
    steps: [{ id: 'restart.kiwix', kind: 'restart_service', status: 'needs_confirm', needs_confirmation: true, title: 'Restart the library', detail: '' }],
  };
  run = await launch({ token: agent.token, env: { ...NO_TRAY, LOCALAI_DESKTOP_TEST_OPEN: 'ensure' } });
  const page = await waitForWindowPage(run.app, isEnsure);
  await page.getByTestId('approve').click();
  await page.getByTestId('confirm-yes').click();
  const pinPage = await waitForWindowPage(run.app, isPinWindow);
  await pinPage.getByTestId('pin-cancel').click();
  await expect(page.getByTestId('approve')).toBeEnabled();
  expect(agent.ensureRequests.filter((r) => r.method === 'POST')).toHaveLength(0);
});

test('the top bar may not call the ensure bridge', async () => {
  run = await launch({ token: agent.token, env: NO_TRAY });
  const bar = (await waitForContents(run.app, isTopBar)).id;
  for (const call of ['view()', 'start()', 'cancel()', "confirm(['whisper.build'])"]) {
    await expect(evaluateIn<unknown>(run.app, bar, `window.localaiDesktop.ensure.${call}`)).rejects.toThrow();
  }
  expect(agent.ensureRequests.filter((r) => r.method !== 'GET')).toHaveLength(0);
});
