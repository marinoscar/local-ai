// The error pages of the start-up sequence, against the fake agent (docs/specs/desktop.md 4.1, 5, 8).
// Each scenario starts the app afresh: no agent (004), no token file (003), a different API major (006), a refused
// login (007). The EACCES cases (001, 002) need a non-root user and a real group, so unit tests cover them.
import { expect, test } from '@playwright/test';
import { ERROR_CODES, ERROR_COPY } from '../../src/main/i18n';
import { type FakeAgent, newFakeToken, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, evaluateIn, isAgentUrl, isErrorPage, launch, pollIn, waitForContents } from './helpers';

let agent: FakeAgent | undefined;
let run: Launched | undefined;
test.afterEach(async () => {
  await run?.stop();
  await agent?.close();
  run = undefined;
  agent = undefined;
});

const bodyText = (id: number) => evaluateIn<string>(run!.app, id, 'document.body.innerText');
const clickButton = (id: number, label: string) =>
  evaluateIn<boolean>(run!.app, id, `(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.includes(${JSON.stringify(label)})); b?.click(); return !!b; })()`);

async function expectErrorPage(code: (typeof ERROR_CODES)[number], lang: 'en' | 'es' = 'en'): Promise<number> {
  const page = await waitForContents(run!.app, (url) => isErrorPage(url) && url.includes(`code=${code}`));
  await pollIn(run!.app, page.id, "document.querySelector('[data-testid=code]') !== null");
  const text = await bodyText(page.id);
  const copy = ERROR_COPY[code];
  expect(text).toContain(code);
  // Both languages are on the page, the chosen one first; the fix and the Portal button too.
  for (const l of ['en', 'es'] as const) {
    expect(text).toContain(copy.title[l]);
    expect(text).toContain(copy.message[l]);
    expect(text).toContain(copy.fix[l]);
  }
  expect(text.indexOf(copy.title[lang])).toBeLessThan(text.indexOf(copy.title[lang === 'en' ? 'es' : 'en']));
  expect(text).toMatch(/Open Survival Portal|Abrir el Portal de Supervivencia/);
  return page.id;
}

test('no agent: LAI-DSK-004 with the unit state, and the Start agent button', async () => {
  run = await launch({ token: newFakeToken() });
  const id = await expectErrorPage('LAI-DSK-004');
  expect(await bodyText(id)).toContain('Start agent');
  expect(await bodyText(id)).toMatch(/Service state: \w+/);
});

test('token file missing: LAI-DSK-003, Portal button opens the Portal without any login', async () => {
  agent = await startFakeAgent({ port: 7070 });
  run = await launch({ token: null });
  const id = await expectErrorPage('LAI-DSK-003');
  expect(agent.sessionRequests).toHaveLength(0);
  expect(await clickButton(id, 'Open Survival Portal')).toBe(true);
  const portal = await waitForContents(run.app, (url) => url === 'http://127.0.0.1:7070/portal');
  await pollIn(run.app, portal.id, "document.getElementById('portal') !== null");
});

test('different API major: LAI-DSK-006, the token is never sent', async () => {
  agent = await startFakeAgent({ port: 7070, version: '1.4.0' });
  run = await launch({ token: newFakeToken() });
  await expectErrorPage('LAI-DSK-006');
  expect(agent.sessionRequests).toHaveLength(0);
});

test('login refused twice: LAI-DSK-007, Copy command puts the fix on the clipboard, Open in browser is offered', async () => {
  agent = await startFakeAgent({ port: 7070 });
  agent.rejectLogin = true;
  run = await launch({ token: newFakeToken() });
  const id = await expectErrorPage('LAI-DSK-007');
  expect(agent.sessionRequests).toHaveLength(2); // the first try and the one after re-reading the token
  const text = await bodyText(id);
  expect(text).toContain('Open in browser');
  expect(text).toContain('aictl ui-code');
  expect(await clickButton(id, 'Copy command')).toBe(true);
  await expect.poll(() => run!.app.evaluate(({ clipboard }) => clipboard.readText())).toBe('sudo aictl token rotate');
});

test('every error page shows its code, both languages, the fix and the Portal button, in the chosen language first', async () => {
  run = await launch({ token: newFakeToken() });
  await expectErrorPage('LAI-DSK-004');
  for (const lang of ['en', 'es'] as const) {
    for (const code of ERROR_CODES) {
      await run.app.evaluate(
        ({ webContents }, url) => webContents.getAllWebContents().find((c) => c.getURL().startsWith('localai://app/error/'))!.loadURL(url),
        `localai://app/error/?code=${code}&lang=${lang}`,
      );
      await expectErrorPage(code, lang);
    }
  }
});

test('a normal start does not show an error page', async () => {
  agent = await startFakeAgent({ port: 7070 });
  run = await launch({ token: agent.token });
  await waitForContents(run.app, isAgentUrl);
  expect((await run.app.evaluate(({ webContents }) => webContents.getAllWebContents().map((c) => c.getURL()))).some(isErrorPage)).toBe(false);
});
