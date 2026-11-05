// Start at login and --hidden (docs/specs/desktop.md 4.5, 3.9). The entry is only written by a packaged app (an
// unpackaged run would point the desktop at /usr/bin/ai-desktop), so these specs need the packaged test copy:
//   LOCALAI_E2E_EXECUTABLE=release/test-copy/ai-desktop xvfb-run -a npx playwright test autostart
// Every launch has its own XDG_CONFIG_HOME, so the entry lands in a temp folder and never in the real ~/.config/autostart.
import { execFile } from 'node:child_process';
import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, executable, isAgentUrl, launch, noSandbox, waitForContents } from './helpers';

const run = promisify(execFile);
let agent: FakeAgent | undefined;
let app: Launched | undefined;

test.skip(executable === undefined, 'the autostart entry is written by the packaged app only (set LOCALAI_E2E_EXECUTABLE to the test copy)');

test.afterEach(async () => {
  await app?.stop();
  app = undefined;
  await agent?.close();
  agent = undefined;
});

const ENTRY = [
  '[Desktop Entry]',
  'Type=Application',
  'Name=Local AI',
  'Exec=/usr/bin/ai-desktop --hidden',
  'Icon=ai-desktop',
  'X-GNOME-Autostart-enabled=true',
  'X-GNOME-Autostart-Delay=5',
  'NoDisplay=false',
  '',
].join('\n');
const TRAY = { LOCALAI_DESKTOP_TRAY: 'available' };
const NO_TRAY = { LOCALAI_DESKTOP_TRAY: 'unavailable' };

const entryPath = (a: Launched): string => join(a.configHome, 'autostart', 'ai-desktop.desktop');
const visibility = (a: Launched): Promise<boolean[]> => a.app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().map((w) => w.isVisible()));

test('first run: the deb entry is written, with --hidden, and nothing else is created in the folder', async () => {
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({ token: agent.token, env: TRAY });
  await waitForContents(app.app, isAgentUrl);
  await expect.poll(() => readFile(entryPath(app!), 'utf8').catch(() => null)).toBe(ENTRY);
  expect(await readdir(join(app.configHome, 'autostart'))).toEqual(['ai-desktop.desktop']);
});

test('settings.autostart false: a stale entry is removed and the user\'s other autostart files stay', async () => {
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({
    token: agent.token,
    env: TRAY,
    seed: async (configHome) => {
      await mkdir(join(configHome, 'ai-desktop'), { recursive: true });
      await writeFile(join(configHome, 'ai-desktop', 'settings.json'), JSON.stringify({ schema: 1, autostart: false }));
      await mkdir(join(configHome, 'autostart'), { recursive: true });
      await writeFile(join(configHome, 'autostart', 'ai-desktop.desktop'), ENTRY);
      await writeFile(join(configHome, 'autostart', 'other-app.desktop'), '[Desktop Entry]\nName=Other\n');
    },
  });
  await waitForContents(app.app, isAgentUrl);
  await expect.poll(() => readdir(join(app!.configHome, 'autostart'))).toEqual(['other-app.desktop']);
});

test('an autostart folder that cannot be created is LAI-DSK-014 in the log, and the app still starts', async () => {
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({
    token: agent.token,
    env: TRAY,
    // A file where the folder should be: it cannot be created, whoever runs the test (root included).
    seed: async (configHome) => writeFile(join(configHome, 'autostart'), 'not a folder'),
  });
  await waitForContents(app.app, isAgentUrl);
  await expect
    .poll(async () => readFile(join(app!.configHome, 'ai-desktop', 'logs', 'main.log'), 'utf8').catch(() => ''), { timeout: 15_000 })
    .toContain('"code":"LAI-DSK-014"');
});

test('--hidden with a tray: the window is not shown; a second launch with --portal brings it forward on the Portal', async () => {
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({ token: agent.token, env: TRAY, args: ['--hidden'] });
  await waitForContents(app.app, isAgentUrl);
  await expect.poll(() => agent!.openEventStreams()).toBe(1);
  await new Promise((r) => setTimeout(r, 500));
  expect(await visibility(app)).toEqual([false]);

  const file = executable!;
  await run(file, [...(noSandbox ? ['--no-sandbox'] : []), '--disable-gpu', '--portal'], { env: app.env, timeout: 30_000 });
  await expect.poll(() => visibility(app!)).toEqual([true]);
  await waitForContents(app.app, (url) => url === 'http://127.0.0.1:7070/portal');
});

test('--hidden with no tray shows the window (there would be no way back to it)', async () => {
  agent = await startFakeAgent({ port: 7070 });
  app = await launch({ token: agent.token, env: NO_TRAY, args: ['--hidden'] });
  await waitForContents(app.app, isAgentUrl);
  await expect.poll(() => visibility(app!)).toEqual([true]);
});

test('--hidden when the node is in survival mode shows the window (the Portal)', async () => {
  agent = await startFakeAgent({ port: 7070, mode: 'survival' });
  app = await launch({ token: agent.token, env: TRAY, args: ['--hidden'] });
  await waitForContents(app.app, (url) => url === 'http://127.0.0.1:7070/portal');
  await expect.poll(() => visibility(app!)).toEqual([true]);
});
