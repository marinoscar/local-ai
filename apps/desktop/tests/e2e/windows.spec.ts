// The main window, the preload bridges and the origin check on IPC (docs/specs/desktop.md 3.4, 3.5, 3.8, 6).
//
//   unpackaged:  npm run build && xvfb-run -a -s "-screen 0 1920x1080x24" npx playwright test tests/e2e/windows.spec.ts
//   packaged:    LOCALAI_E2E_EXECUTABLE=release/test-copy/ai-desktop (as in helpers.ts)
// The bounds tests need a display large enough for a 900x600 window, hence the 1920x1080 screen above.
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, childUrls, evaluateIn, isAgentUrl, isTopBar, launch, openProbeWindow, waitForContents } from './helpers';

let agent: FakeAgent | undefined;
let run: Launched | undefined;
test.afterEach(async () => {
  await run?.stop();
  await agent?.close();
  run = undefined;
  agent = undefined;
});

const settingsFile = (r: Launched) => join(r.configHome, 'ai-desktop', 'settings.json');
const readSettings = async (r: Launched): Promise<Record<string, any>> => JSON.parse(await readFile(settingsFile(r), 'utf8').catch(() => '{}')) as Record<string, any>;

async function start(options: Parameters<typeof launch>[0] = {}): Promise<{ agentPage: number; bar: number }> {
  agent = await startFakeAgent({ port: 7070 });
  run = await launch({ token: agent.token, ...options });
  const agentPage = (await waitForContents(run.app, isAgentUrl)).id;
  const bar = (await waitForContents(run.app, isTopBar)).id;
  return { agentPage, bar };
}

const settle = (ms = 600) => new Promise((r) => setTimeout(r, ms));

test.describe('the bridges', () => {
  test('a shell page sees the LocalaiDesktop surface and no Node; an agent page sees exactly the 5 host members', async () => {
    const { agentPage, bar } = await start();
    const shell = await evaluateIn<string>(run!.app, bar, 'JSON.stringify({ keys: Object.keys(window.localaiDesktop).sort(), host: typeof window.localaiDesktopHost, node: [typeof require, typeof process, typeof module, typeof Buffer] })');
    expect(JSON.parse(shell)).toEqual({
      keys: ['closePortal', 'cloud', 'connectionInfo', 'copyApiKey', 'errorAction', 'lang', 'onNotice', 'onStatus', 'openWindow', 'pinSubmit', 'revealKey', 'rotateKey', 'setFullScreen', 'setMode', 'settings', 'showMenu', 'showTab', 'startAgent', 'status'].sort(),
      host: 'undefined',
      node: ['undefined', 'undefined', 'undefined', 'undefined'],
    });
    const version = await run!.app.evaluate(({ app }) => app.getVersion());
    const host = await evaluateIn<string>(run!.app, agentPage, 'JSON.stringify({ keys: Object.keys(window.localaiDesktopHost), shell: typeof window.localaiDesktop, isDesktop: window.localaiDesktopHost.isDesktop, version: window.localaiDesktopHost.version, node: [typeof require, typeof process, typeof module, typeof Buffer] })');
    expect(JSON.parse(host)).toEqual({
      keys: ['isDesktop', 'version', 'copyApiKey', 'openWindow', 'connectionInfo'],
      shell: 'undefined',
      isDesktop: true,
      version,
      node: ['undefined', 'undefined', 'undefined', 'undefined'],
    });
  });

  test('the agent UI can open a window through the host bridge and copy the key (it never receives it)', async () => {
    const { agentPage } = await start();
    await evaluateIn(run!.app, agentPage, "window.localaiDesktopHost.openWindow('maps')");
    await waitForContents(run!.app, (url) => url === 'http://127.0.0.1:7070/maps/');
    expect(await evaluateIn<string>(run!.app, agentPage, "window.localaiDesktopHost.copyApiKey().then((r) => JSON.stringify(r))")).toBe('{"ok":true,"clearsInS":60}');
    expect(await run!.app.evaluate(({ clipboard }) => clipboard.readText())).toBe(agent!.apiKey);
    // The connection info it can read has the key masked.
    const info = await evaluateIn<string>(run!.app, agentPage, "window.localaiDesktopHost.connectionInfo().then((r) => JSON.stringify(r))");
    expect(info).toContain(`${agent!.apiKey.slice(0, 4)}…${agent!.apiKey.slice(-4)}`);
    expect(info).not.toContain(agent!.apiKey);
    // A name that is not one of the three opens nothing.
    await evaluateIn(run!.app, agentPage, "window.localaiDesktopHost.openWindow('../api/v1/key')");
    await settle();
    expect(await childUrls(run!.app)).toEqual(['http://127.0.0.1:7070/maps/']);
  });

  test('the top bar reads and changes the settings through the allowlist; cloud.* is refused', async () => {
    const { bar } = await start();
    const keys = await evaluateIn<string>(run!.app, bar, 'window.localaiDesktop.settings.get().then((s) => JSON.stringify(Object.keys(s).sort()))');
    expect(JSON.parse(keys)).toEqual(['autostart', 'cloud', 'lang', 'secrets', 'updates', 'window']);
    const changed = await evaluateIn<string>(run!.app, bar, "window.localaiDesktop.settings.set({ autostart: false, lang: 'es' }).then((s) => JSON.stringify([s.autostart, s.lang]))");
    expect(JSON.parse(changed)).toEqual([false, 'es']);
    expect((await readSettings(run!)).autostart).toBe(false);
    const refused = await evaluateIn<string>(run!.app, bar, "window.localaiDesktop.settings.set({ cloud: { linked: true } }).then(() => 'accepted', (e) => 'refused')");
    expect(refused).toBe('refused');
    expect((await readSettings(run!)).cloud?.linked).not.toBe(true);
    // The language is read at once, synchronously.
    expect(await evaluateIn<string>(run!.app, bar, 'window.localaiDesktop.lang()')).toBe('es');
  });
});

test.describe('IPC from a page that may not call is refused, with no side effect', () => {
  const probes: Array<[string, { url: string; preload: 'shell' | 'agent'; partition?: string }]> = [
    ['a data: page', { url: 'data:text/html,<title>probe</title><p>probe</p>', preload: 'shell' }],
    ['an agent page given the shell preload', { url: 'http://127.0.0.1:7070/chat/', preload: 'shell', partition: 'persist:agent' }],
  ];
  for (const [name, probe] of probes) {
    test(name, async () => {
      await start();
      const id = await openProbeWindow(run!.app, probe);
      const outcome = (code: string) => evaluateIn<string>(run!.app, id, `(async () => { try { await (${code}); return 'accepted'; } catch (e) { return 'refused'; } })()`);
      // The bridge object is there (the preload ran), but every handler refuses this page.
      expect(await evaluateIn<string>(run!.app, id, 'typeof window.localaiDesktop')).toBe('object');
      expect(await outcome("window.localaiDesktop.status()")).toBe('refused');
      expect(await outcome("window.localaiDesktop.settings.get()")).toBe('refused');
      expect(await outcome("window.localaiDesktop.settings.set({ autostart: false })")).toBe('refused');
      expect(await outcome("window.localaiDesktop.setMode('survival')")).toBe('refused');
      expect(await outcome("window.localaiDesktop.errorAction('open-portal', 'LAI-DSK-004')")).toBe('refused');
      // Fire-and-forget calls and the synchronous one: nothing happens, the language falls back.
      await evaluateIn(run!.app, id, "window.localaiDesktop.openWindow('chat'); window.localaiDesktop.showTab('lab'); window.localaiDesktop.showMenu();");
      expect(await evaluateIn<string>(run!.app, id, 'window.localaiDesktop.lang()')).toBe('en');
      await settle();
      expect((await childUrls(run!.app)).filter((u) => u.endsWith('/chat/') && !probe.url.endsWith('/chat/'))).toEqual([]);
      // The refused settings write left no trace, and the real top bar still works.
      expect((await readSettings(run!)).autostart).not.toBe(false);
      const bar = (await waitForContents(run!.app, isTopBar)).id;
      expect(await evaluateIn<string>(run!.app, bar, "window.localaiDesktop.status().then((s) => s.agent)")).toBe('up');
    });
  }

  test('the host bridge refuses a page that is not the agent origin', async () => {
    await start();
    const id = await openProbeWindow(run!.app, { url: 'data:text/html,<p>probe</p>', preload: 'agent' });
    expect(await evaluateIn<string>(run!.app, id, 'JSON.stringify(Object.keys(window.localaiDesktopHost))')).toContain('openWindow');
    expect(await evaluateIn<string>(run!.app, id, "window.localaiDesktopHost.version")).toBe('');
    await evaluateIn(run!.app, id, "window.localaiDesktopHost.openWindow('library')");
    expect(await evaluateIn<string>(run!.app, id, "window.localaiDesktopHost.copyApiKey().then(() => 'accepted', () => 'refused')")).toBe('refused');
    await settle();
    expect(await childUrls(run!.app)).toEqual([]);
  });
});

test.describe('window bounds and layout', () => {
  const workArea = (r: Launched) => r.app.evaluate(({ screen }) => screen.getPrimaryDisplay().workArea);
  const mainBounds = (r: Launched) => r.app.evaluate(({ BaseWindow }) => BaseWindow.getAllWindows()[0]!.getBounds());

  test('the top bar and the content stay usable at the minimum size, 900x600', async () => {
    const { bar } = await start();
    const area = await workArea(run!);
    test.skip(area.width < 900 || area.height < 600, 'the display is smaller than the minimum window size');
    await run!.app.evaluate(({ BaseWindow }) => BaseWindow.getAllWindows()[0]!.setContentSize(900, 600));
    const layout = () =>
      run!.app.evaluate(({ BaseWindow }) =>
        BaseWindow.getAllWindows()[0]!.contentView.children.map((v) => (v as unknown as { getBounds(): Record<string, number> }).getBounds()),
      );
    await expect.poll(async () => (await layout()).map((b) => [b['width'], b['height']])).toEqual([[900, 48], [900, 552]]);
    const [barBounds, contentBounds] = await layout();
    expect(barBounds).toMatchObject({ x: 0, y: 0 });
    expect(contentBounds).toMatchObject({ x: 0, y: 48 });
    // Nothing of the bar is clipped or scrolls sideways, in either language.
    for (const lang of ['en', 'es'] as const) {
      await evaluateIn(run!.app, bar, `window.localaiDesktop.settings.set({ lang: '${lang}' })`);
      await settle(300);
      const fits = await evaluateIn<string>(
        run!.app,
        bar,
        `JSON.stringify({ scroll: document.documentElement.scrollWidth <= window.innerWidth, menu: document.querySelector('[data-testid=menu]').getBoundingClientRect().right <= window.innerWidth, copy: document.querySelector('[data-testid=copy-key]').getBoundingClientRect().right <= window.innerWidth })`,
      );
      expect(JSON.parse(fits), lang).toEqual({ scroll: true, menu: true, copy: true });
    }
  });

  test('the bounds are saved, and restored after a restart', async () => {
    await start();
    const area = await workArea(run!);
    test.skip(area.width < 1100 || area.height < 800, 'the display is too small for this test');
    await run!.app.evaluate(({ BaseWindow }, a) => BaseWindow.getAllWindows()[0]!.setBounds({ x: a.x + 40, y: a.y + 30, width: 1000, height: 700 }), area);
    await expect
      .poll(async () => (await readSettings(run!)).window?.bounds, { timeout: 10_000 })
      .toMatchObject({ width: 1000, height: 700, maximized: false });
    const saved = (await readSettings(run!)).window.bounds as { x: number; y: number };
    const work = run!.work;
    await run!.stop({ keep: true });
    await agent!.close();

    agent = await startFakeAgent({ port: 7070 });
    run = await launch({ token: agent.token, work });
    await waitForContents(run.app, isAgentUrl);
    const bounds = await mainBounds(run);
    expect(bounds).toMatchObject({ width: 1000, height: 700 });
    expect(Math.abs(bounds.x - saved.x)).toBeLessThanOrEqual(40);
    expect(Math.abs(bounds.y - saved.y)).toBeLessThanOrEqual(40);
    // Cleanup of the reused folder is the first run's job: remove it through a plain launch-less stop.
    await run.stop();
    const { rm } = await import('node:fs/promises');
    await rm(work, { recursive: true, force: true });
  });

  test('bounds saved on a display that is gone are clamped onto the visible one', async () => {
    agent = await startFakeAgent({ port: 7070 });
    run = await launch({ token: agent.token, settings: { schema: 1, window: { bounds: { x: 40000, y: -30000, width: 1000, height: 700 } } } });
    await waitForContents(run.app, isAgentUrl);
    const area = await workArea(run);
    test.skip(area.width < 900 || area.height < 600, 'the display is smaller than the minimum window size');
    const bounds = await mainBounds(run);
    expect(bounds.x).toBeGreaterThanOrEqual(area.x);
    expect(bounds.y).toBeGreaterThanOrEqual(area.y);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(area.x + area.width);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(area.y + area.height);
  });
});
