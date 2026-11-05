// Updates are off by default and cost nothing (docs/specs/desktop.md 4.10, 9). Against the real app (unpackaged, or the
// packaged test copy with LOCALAI_E2E_EXECUTABLE) and the fake agent: with the box unticked electron-updater is never
// loaded and nothing is blocked or sent; and the bundled library, once loaded, has the HTTP executor that
// src/main/updates.ts puts on the guarded `updater` session (the unit tests cover the controller; no test here talks
// to GitHub).
import { expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, isAgentUrl, launch, waitForContents } from './helpers';

let agent: FakeAgent | undefined;
let app: Launched | undefined;

test.afterEach(async () => {
  await app?.stop();
  app = undefined;
  await agent?.close();
  agent = undefined;
});

/** The main process's loaded modules whose path names the updater chunk or the library (`Module._cache` of the entry module). */
const loadedUpdaterModules = (a: Launched): Promise<string[]> =>
  a.app.evaluate(() => {
    const cache = (process.mainModule!.constructor as unknown as { _cache: Record<string, unknown> })._cache;
    const keys = Object.keys(cache);
    // The cache must be the real one (the entry bundle is in it), or an empty answer would prove nothing.
    if (!keys.some((key) => /[\\/]out[\\/]main[\\/]index\.js$/.test(key))) throw new Error('Module._cache does not hold the entry bundle');
    return keys.filter((key) => /electron-updater|[\\/]chunks[\\/]main-/.test(key));
  });

test('default settings: electron-updater is not loaded, and nothing was blocked or sent', async () => {
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({ token: agent.token });
  await waitForContents(app.app, isAgentUrl);
  await new Promise((r) => setTimeout(r, 1500));
  expect(await loadedUpdaterModules(app)).toEqual([]);
  expect(app.stderr()).not.toContain('"component":"updates"');
  expect(app.stderr()).not.toContain('LAI-DSK-012');
  expect(app.stderr()).not.toContain('LAI-DSK-016');
});

test('box ticked in an unpackaged run: still no check (updates only work in the installed app)', async () => {
  test.skip(process.env['LOCALAI_E2E_EXECUTABLE'] !== undefined, 'a packaged copy would really start checking GitHub');
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({ token: agent.token, settings: { schema: 1, updates: { checkForUpdates: true } } });
  await waitForContents(app.app, isAgentUrl);
  await new Promise((r) => setTimeout(r, 1500));
  expect(await loadedUpdaterModules(app)).toEqual([]);
  expect(app.stderr()).not.toContain('LAI-DSK-012');
});

test('the bundled electron-updater loads in Electron and has the HTTP executor the guard relies on', async () => {
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({ token: agent.token });
  await waitForContents(app.app, isAgentUrl);
  const shape = await app.app.evaluate(async ({ app: electronApp }) => {
    const entry = process.mainModule!;
    const path = entry.require('node:path') as typeof import('node:path');
    const fs = entry.require('node:fs') as typeof import('node:fs');
    const dir = path.join(electronApp.getAppPath(), 'out', 'main', 'chunks');
    const chunk = fs.readdirSync(dir).find((name) => name.startsWith('main-') && name.endsWith('.js'));
    if (!chunk) return { chunk: null };
    const loaded = entry.require(path.join(dir, chunk)) as { default: { autoUpdater: Record<string, unknown> } };
    const updater = loaded.default.autoUpdater as { checkForUpdates?: unknown; setFeedURL?: unknown; httpExecutor?: Record<string, unknown>; autoDownload?: unknown };
    return {
      chunk,
      checkForUpdates: typeof updater.checkForUpdates,
      setFeedURL: typeof updater.setFeedURL,
      executor: updater.httpExecutor !== undefined && 'cachedSession' in updater.httpExecutor,
    };
  });
  expect(shape).toMatchObject({ checkForUpdates: 'function', setFeedURL: 'function', executor: true });
});
