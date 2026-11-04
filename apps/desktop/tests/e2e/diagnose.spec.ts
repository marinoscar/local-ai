// `ai-desktop --diagnose` (docs/specs/desktop.md 3.9, 7): JSON on stdout, exit code 0, no window, no display.
import { execFile } from 'node:child_process';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { expect, test } from '@playwright/test';
import { type FakeAgent, newFakeToken, startFakeAgent } from '../fixtures/fake-agent';
import { appDir, executable, noSandbox } from './helpers';

const run = promisify(execFile);
let agent: FakeAgent | undefined;
let work: string;
test.beforeEach(async () => {
  work = await mkdtemp(join(tmpdir(), 'ai-desktop-diag-e2e-'));
});
test.afterEach(async () => {
  await agent?.close();
  agent = undefined;
  await rm(work, { recursive: true, force: true });
});

/** LAI-DSK-015 depends on the machine's AppArmor (a runner may restrict user namespaces and the test copy has no profile). */
const withoutSandboxCheck = (checks: Array<{ code: string }>): Array<{ code: string }> => checks.filter((c) => c.code !== 'LAI-DSK-015');

/** Runs the app with --diagnose and NO display, as `ai-desktop --diagnose | jq .` does in a terminal over ssh. */
async function diagnose(tokenPath: string): Promise<{ report: Record<string, any>; code: number; stderr: string }> {
  const env = { ...process.env, XDG_CONFIG_HOME: join(work, 'config'), LOCALAI_DESKTOP_TOKEN_PATH: tokenPath } as Record<string, string | undefined>;
  delete env['DISPLAY'];
  delete env['WAYLAND_DISPLAY'];
  const args = [...(noSandbox ? ['--no-sandbox'] : []), ...(executable ? [] : [appDir]), '--diagnose'];
  const file = executable ?? require('electron');
  const result = await run(file as string, args, { env, timeout: 30_000 }).then(
    (ok) => ({ code: 0, ...ok }),
    (error: { code?: number; stdout?: string; stderr?: string }) => ({ code: error.code ?? 1, stdout: error.stdout ?? '', stderr: error.stderr ?? '' }),
  );
  return { report: JSON.parse(result.stdout) as Record<string, any>, code: result.code, stderr: result.stderr };
}

test('prints the documented keys, exits 0 without a display, finds nothing wrong with a healthy agent', async () => {
  const token = newFakeToken();
  agent = await startFakeAgent({ port: 7070, token });
  const tokenPath = join(work, 'agent-token');
  await writeFile(tokenPath, `${token}\n`);
  const { report, code } = await diagnose(tokenPath);
  expect(code).toBe(0);
  expect(Object.keys(report)).toEqual(['version', 'electron', 'agent', 'token', 'session', 'tray', 'safe_storage_backend', 'cloud', 'egress_blocked_total', 'autostart', 'checks']);
  expect(report['agent']).toMatchObject({ reachable: true, version: '0.1.0' });
  expect(report['token']).toMatchObject({ readable: true, needs_relogin: false });
  expect(report['session']).toBe('ok');
  expect(withoutSandboxCheck(report['checks'])).toEqual([]);
  // The token never reaches the output, and diagnosing did not open a session the cookie of which nobody keeps.
  expect(JSON.stringify(report)).not.toContain(token);
  expect(agent.sessionRequests).toHaveLength(0);
});

test('with no agent and no token file the checks say what to do and the exit code is still 0', async () => {
  const { report, code } = await diagnose(join(work, 'missing-token'));
  expect(code).toBe(0);
  expect(report['agent'].reachable).toBe(false);
  expect(report['session']).toBe('skipped');
  expect(withoutSandboxCheck(report['checks']).map((c) => c.code).sort()).toEqual(['LAI-DSK-003', 'LAI-DSK-004']);
  for (const check of report['checks']) {
    expect(Object.keys(check)).toEqual(['code', 'severity', 'title', 'fix']);
    expect(check.fix.length).toBeGreaterThan(10);
  }
});
