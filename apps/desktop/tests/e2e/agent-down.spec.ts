// "Start agent" from the LAI-DSK-004 page, with systemctl and pkexec replaced by stub scripts
// (LOCALAI_DESKTOP_SYSTEMCTL / LOCALAI_DESKTOP_PKEXEC, honoured only by unpackaged builds and the e2e test copy).
// The real systemctl and pkexec are never run: if a stub is missing the app refuses to run pkexec at all.
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { type FakeAgent, newFakeToken, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, evaluateIn, isAgentUrl, isErrorPage, launch, pollIn, waitForContents } from './helpers';

let agent: FakeAgent | undefined;
let run: Launched | undefined;
let stubs: string | undefined;
test.afterEach(async () => {
  await run?.stop();
  await agent?.close();
  if (stubs) await rm(stubs, { recursive: true, force: true });
  run = undefined;
  agent = undefined;
  stubs = undefined;
});

const bodyText = (id: number) => evaluateIn<string>(run!.app, id, 'document.body.innerText');
const clickButton = (id: number, label: string) =>
  evaluateIn<boolean>(run!.app, id, `(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.includes(${JSON.stringify(label)})); b?.click(); return !!b; })()`);

interface Stubs {
  dir: string;
  systemctl: string;
  pkexec: string;
  calls(): Promise<string[]>;
}

/**
 * Two shell scripts. `start` behaves as `startMode` says (`ok`, `auth`, `missing`); `status` prints a fixed excerpt
 * with a token-shaped string (it must be scrubbed); `is-active` answers `inactive`. pkexec exits `pkexecExit`.
 * Every call is appended to calls.log.
 */
async function makeStubs(startMode: 'ok' | 'auth' | 'missing', pkexecExit = 126): Promise<Stubs> {
  const dir = await mkdtemp(join(tmpdir(), 'ai-desktop-stubs-'));
  stubs = dir;
  const leak = `lat_${'Zz9'.repeat(15)}_`;
  const systemctl = join(dir, 'systemctl');
  const pkexec = join(dir, 'pkexec');
  await writeFile(
    systemctl,
    `#!/bin/sh
dir="$(dirname "$0")"
echo "systemctl $*" >> "$dir/calls.log"
case "$1" in
  is-active) echo inactive; exit 3 ;;
  status) printf 'localai-agent.service - Local AI agent\\n   Active: failed (Result: exit-code)\\n   credential ${leak}\\n'; exit 3 ;;
  start)
    case "${startMode}" in
      ok) touch "$dir/started"; exit 0 ;;
      auth) echo 'Failed to start localai-agent.service: Interactive authentication required.' >&2; exit 1 ;;
      missing) echo 'Failed to start localai-agent.service: Unit localai-agent.service not found.' >&2; exit 5 ;;
    esac ;;
esac
exit 0
`,
  );
  await writeFile(pkexec, `#!/bin/sh\necho "pkexec $*" >> "$(dirname "$0")/calls.log"\nexit ${pkexecExit}\n`);
  await chmod(systemctl, 0o755);
  await chmod(pkexec, 0o755);
  return {
    dir,
    systemctl,
    pkexec,
    calls: async () => (await readFile(join(dir, 'calls.log'), 'utf8').catch(() => '')).split('\n').filter((l) => l !== ''),
  };
}

async function openDownPage(s: Stubs): Promise<number> {
  run = await launch({ token: newFakeToken(), env: { LOCALAI_DESKTOP_SYSTEMCTL: s.systemctl, LOCALAI_DESKTOP_PKEXEC: s.pkexec } });
  const page = await waitForContents(run.app, (url) => isErrorPage(url) && url.includes('code=LAI-DSK-004'));
  await pollIn(run.app, page.id, "[...document.querySelectorAll('button')].some((b) => b.textContent.includes('Start agent'))");
  return page.id;
}

test('no agent: Start agent runs systemctl (no pkexec), the agent comes up, the dashboard opens', async () => {
  const s = await makeStubs('ok');
  const id = await openDownPage(s);
  expect(await bodyText(id)).toContain('LAI-DSK-004');
  expect(await clickButton(id, 'Start agent')).toBe(true);
  // The button shows the working state while the app waits for the agent.
  await pollIn(run!.app, id, "document.body.innerText.includes('Starting')");

  // The stub "started" the unit: now the fake agent appears, as the real one would.
  await expect.poll(() => s.calls().then((c) => c.includes('systemctl start --no-ask-password localai-agent.service'))).toBe(true);
  const token = (await readFile(run!.tokenPath, 'utf8')).trim();
  agent = await startFakeAgent({ port: 7070, token });

  const dashboard = await waitForContents(run!.app, isAgentUrl, 30_000);
  await pollIn(run!.app, dashboard.id, "document.getElementById('dashboard') !== null");
  await pollIn(run!.app, dashboard.id, "document.getElementById('node').textContent.startsWith('node ')");
  expect((await s.calls()).filter((c) => c.startsWith('pkexec'))).toEqual([]);
});

test('authorization needed: pkexec is asked; dismissed (exit 126) shows LAI-DSK-005 with the scrubbed status excerpt', async () => {
  const s = await makeStubs('auth', 126);
  const id = await openDownPage(s);
  expect(await clickButton(id, 'Start agent')).toBe(true);
  await pollIn(run!.app, id, "document.querySelector('[data-testid=start-failure]') !== null");

  const text = await bodyText(id);
  expect(text).toContain('LAI-DSK-005');
  expect(text).toContain('sudo aictl doctor');
  const excerpt = await evaluateIn<string>(run!.app, id, "document.querySelector('[data-testid=status-excerpt]').textContent");
  expect(excerpt).toContain('Active: failed');
  expect(excerpt).not.toMatch(/lat_/);
  // The button is usable again.
  expect(await evaluateIn<boolean>(run!.app, id, "[...document.querySelectorAll('button')].find((b) => b.textContent.includes('Start agent'))?.disabled === false")).toBe(true);

  const calls = await s.calls();
  expect(calls.filter((c) => c.startsWith('pkexec'))).toEqual([`pkexec ${s.systemctl} start localai-agent.service`]);
  expect(calls).toContain('systemctl status localai-agent.service --no-pager -n 20');
});

test('unit missing: no pkexec, LAI-DSK-005 at once, and Copy command copies the doctor command', async () => {
  const s = await makeStubs('missing');
  const id = await openDownPage(s);
  expect(await clickButton(id, 'Start agent')).toBe(true);
  await pollIn(run!.app, id, "document.querySelector('[data-testid=start-failure]') !== null");
  expect((await s.calls()).some((c) => c.startsWith('pkexec'))).toBe(false);

  expect(await evaluateIn<boolean>(run!.app, id, "(() => { const b = document.querySelector('[data-testid=start-failure] button'); b?.click(); return !!b; })()")).toBe(true);
  await expect.poll(() => run!.app.evaluate(({ clipboard }) => clipboard.readText())).toBe('sudo aictl doctor');
});
