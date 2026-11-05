import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  AGENT_UNIT,
  type ExecFn,
  type ExecResult,
  type StartAgentDeps,
  type StartBinaries,
  createAgentSupervisor,
  execFileRunner,
  isAuthorizationError,
  resolveStartBinaries,
  scrubExcerpt,
  startAgent,
} from '../../src/main/agent-supervisor';
import { NULL_LOGGER } from '../../src/main/log';

// desktop.md 4.2 "Start agent": systemctl, then pkexec, then 30 s of /health. Nothing here touches the real system:
// every call goes through a fake `exec`, and the clock is fake.

const SYSTEMCTL = '/usr/bin/systemctl';
const PKEXEC = '/usr/bin/pkexec';
const REAL: StartBinaries = { systemctl: SYSTEMCTL, pkexec: PKEXEC };
const STATUS_ARGS = ['status', AGENT_UNIT, '--no-pager', '-n', '20'];
const STATUS_TEXT = '● localai-agent.service - Local AI agent\n     Active: inactive (dead)\n';

const result = (over: Partial<ExecResult> = {}): ExecResult => ({ code: 0, stdout: '', stderr: '', timedOut: false, ...over });

interface Call {
  file: string;
  args: readonly string[];
  timeoutMs: number;
}

/** A fake exec: answers by program and first argument; records every call. */
function harness(options: {
  start?: Partial<ExecResult>;
  pkexec?: Partial<ExecResult>;
  status?: Partial<ExecResult>;
  state?: string;
  /** The agent answers /health from this poll on (1-based); never when absent. */
  healthyAtPoll?: number;
  binaries?: StartBinaries;
}) {
  const calls: Call[] = [];
  const sleeps: number[] = [];
  let clock = 0;
  let polls = 0;
  const exec: ExecFn = async (file, args, o) => {
    calls.push({ file, args, timeoutMs: o.timeoutMs });
    if (args.includes('status')) return result({ stdout: STATUS_TEXT, code: 3, ...options.status });
    if (file.endsWith('pkexec')) return result(options.pkexec ?? {});
    return result(options.start ?? {});
  };
  const deps: StartAgentDeps = {
    binaries: options.binaries ?? REAL,
    exec,
    health: async () => {
      polls += 1;
      return options.healthyAtPoll !== undefined && polls >= options.healthyAtPoll;
    },
    unitState: async () => options.state ?? 'inactive',
    sleep: async (ms) => {
      sleeps.push(ms);
      clock += ms;
    },
    now: () => clock,
    log: NULL_LOGGER,
  };
  return { deps, calls, sleeps, polls: () => polls, clock: () => clock };
}

const programs = (calls: Call[]): string[] => calls.map((c) => `${c.file} ${c.args.join(' ')}`);

describe('startAgent: the outcomes of desktop.md 4.2', () => {
  it('already active and answering: nothing is run, no pkexec', async () => {
    const h = harness({ state: 'active', healthyAtPoll: 1 });
    expect(await startAgent(h.deps)).toEqual({ ok: true });
    expect(h.calls).toEqual([]);
  });

  it('activating counts as active: the unit is not started again, the wait covers it', async () => {
    const h = harness({ state: 'activating', healthyAtPoll: 3 });
    expect(await startAgent(h.deps)).toEqual({ ok: true });
    expect(h.calls).toEqual([]);
    expect(h.sleeps).toEqual([1000, 1000]);
  });

  it('active but never answering: waits the 30 s, then LAI-DSK-005 with the status excerpt and no start call', async () => {
    const h = harness({ state: 'active' });
    const out = await startAgent(h.deps);
    expect(out).toMatchObject({ ok: false, code: 'LAI-DSK-005', reason: 'timeout' });
    expect(programs(h.calls)).toEqual([`${SYSTEMCTL} ${STATUS_ARGS.join(' ')}`]);
  });

  it('systemctl succeeds (polkit rule present): no pkexec, then waits for /health once a second', async () => {
    const h = harness({ healthyAtPoll: 4 });
    expect(await startAgent(h.deps)).toEqual({ ok: true });
    expect(programs(h.calls)).toEqual([`${SYSTEMCTL} start --no-ask-password ${AGENT_UNIT}`]);
    expect(h.sleeps).toEqual([1000, 1000, 1000]);
    expect(h.polls()).toBe(4);
  });

  it.each([
    ['Interactive authentication required.', 1],
    ['Failed to start localai-agent.service: Interactive authentication required.\nSee system logs and \'systemctl status localai-agent.service\' for details.', 1],
    ['Failed to start localai-agent.service: Access denied', 1],
    ['Access denied', 4],
  ])('polkit denial (%j, exit %i): pkexec runs with exactly the fixed arguments, then the wait', async (stderr, code) => {
    const h = harness({ start: { code, stderr }, healthyAtPoll: 2 });
    expect(await startAgent(h.deps)).toEqual({ ok: true });
    expect(h.calls.map((c) => [c.file, c.args])).toEqual([
      [SYSTEMCTL, ['start', '--no-ask-password', AGENT_UNIT]],
      [PKEXEC, [SYSTEMCTL, 'start', AGENT_UNIT]],
    ]);
  });

  it.each([
    [126, 'not-authorized'],
    [127, 'not-authorized'],
    [1, 'pkexec-failed'],
  ] as const)('pkexec exits %i: LAI-DSK-005 (%s) with the read-only status excerpt, and the agent is never waited for', async (code, reason) => {
    const h = harness({ start: { code: 1, stderr: 'Interactive authentication required.' }, pkexec: { code } });
    expect(await startAgent(h.deps)).toEqual({ ok: false, code: 'LAI-DSK-005', reason, statusExcerpt: STATUS_TEXT.trim() });
    expect(h.calls.at(-1)).toMatchObject({ file: SYSTEMCTL, args: STATUS_ARGS });
    expect(h.polls()).toBe(0);
  });

  it('pkexec that ran past its timeout is a failure too', async () => {
    const h = harness({ start: { code: 1, stderr: 'Interactive authentication required.' }, pkexec: { code: null, timedOut: true } });
    expect(await startAgent(h.deps)).toMatchObject({ ok: false, code: 'LAI-DSK-005', reason: 'timeout' });
  });

  it('unit missing: pkexec is skipped (a password cannot fix it) and the excerpt is shown at once', async () => {
    const h = harness({ start: { code: 5, stderr: 'Failed to start localai-agent.service: Unit localai-agent.service not found.' }, status: { stdout: 'Unit localai-agent.service could not be found.\n', code: 4 } });
    expect(await startAgent(h.deps)).toEqual({ ok: false, code: 'LAI-DSK-005', reason: 'start-failed', statusExcerpt: 'Unit localai-agent.service could not be found.' });
    expect(h.calls.map((c) => c.file)).toEqual([SYSTEMCTL, SYSTEMCTL]);
    expect(programs(h.calls).some((p) => p.includes('pkexec'))).toBe(false);
    expect(h.polls()).toBe(0);
  });

  it('the unit failed to start (job failed): no pkexec', async () => {
    const h = harness({ start: { code: 1, stderr: 'Job for localai-agent.service failed because the control process exited with error code.' } });
    expect(await startAgent(h.deps)).toMatchObject({ ok: false, reason: 'start-failed' });
    expect(programs(h.calls).some((p) => p.includes('pkexec'))).toBe(false);
  });

  it('systemctl start timing out is not an authorization problem: no pkexec', async () => {
    const h = harness({ start: { code: null, timedOut: true } });
    expect(await startAgent(h.deps)).toMatchObject({ ok: false, reason: 'timeout' });
    expect(programs(h.calls).some((p) => p.includes('pkexec'))).toBe(false);
  });

  it('no systemctl at all (ENOENT) ends in LAI-DSK-005 without pkexec', async () => {
    const h = harness({ start: { code: 'ENOENT', stderr: '' }, status: { code: 'ENOENT', stdout: '', stderr: '' } });
    const out = await startAgent(h.deps);
    expect(out).toMatchObject({ ok: false, code: 'LAI-DSK-005', reason: 'start-failed' });
    expect(out).not.toHaveProperty('statusExcerpt');
  });

  it('health never comes up (fake clock): polled every second for 30 s, then LAI-DSK-005 with the excerpt', async () => {
    const h = harness({});
    const out = await startAgent(h.deps);
    expect(out).toEqual({ ok: false, code: 'LAI-DSK-005', reason: 'timeout', statusExcerpt: STATUS_TEXT.trim() });
    expect(h.clock()).toBe(30_000);
    expect(h.sleeps.every((ms) => ms === 1000)).toBe(true);
    expect(h.sleeps).toHaveLength(30);
    expect(h.polls()).toBe(31);
    expect(h.calls.at(-1)).toMatchObject({ file: SYSTEMCTL, args: STATUS_ARGS });
  });

  it('health comes up on the last second: still a success', async () => {
    const h = harness({ healthyAtPoll: 31 });
    expect(await startAgent(h.deps)).toEqual({ ok: true });
  });

  it('a systemctl override without a pkexec override never runs the real pkexec', async () => {
    const h = harness({ start: { code: 1, stderr: 'Interactive authentication required.' }, binaries: { systemctl: '/tmp/stub-systemctl', pkexec: null } });
    expect(await startAgent(h.deps)).toMatchObject({ ok: false, reason: 'pkexec-unavailable' });
    expect(h.calls.map((c) => c.file)).toEqual(['/tmp/stub-systemctl', '/tmp/stub-systemctl']);
  });

  it('with both overrides, pkexec runs the stub systemctl', async () => {
    const h = harness({ start: { code: 1, stderr: 'Access denied' }, pkexec: { code: 126 }, binaries: { systemctl: '/tmp/stub-systemctl', pkexec: '/tmp/stub-pkexec' } });
    await startAgent(h.deps);
    expect(h.calls[1]).toMatchObject({ file: '/tmp/stub-pkexec', args: ['/tmp/stub-systemctl', 'start', AGENT_UNIT] });
  });

  it('the excerpt never carries a token', async () => {
    const lat = `lat_${randomBytes(32).toString('base64url')}`;
    const hex = randomBytes(32).toString('hex');
    const h = harness({ start: { code: 5, stderr: 'Unit not found.' }, status: { stdout: `line one\nExecStart=agent --credential ${lat}\nid ${hex}\n` } });
    const out = await startAgent(h.deps);
    expect(out.ok).toBe(false);
    const excerpt = (out as { statusExcerpt?: string }).statusExcerpt ?? '';
    expect(excerpt).toContain('line one');
    expect(excerpt).not.toContain(lat);
    expect(excerpt).not.toContain(hex);
  });
});

describe('classification and scrubbing helpers', () => {
  it.each([
    [{ code: 1, stderr: 'Failed to start x.service: Interactive authentication required.', stdout: '' }, true],
    [{ code: 1, stderr: 'Access denied', stdout: '' }, true],
    [{ code: 1, stderr: 'Failed to start x.service: Unit x.service not found.', stdout: '' }, false],
    [{ code: 5, stderr: 'Unit x.service not found.', stdout: '' }, false],
    [{ code: 1, stderr: 'Job for x.service failed.', stdout: '' }, false],
    [{ code: 'ENOENT', stderr: '', stdout: '' }, false],
    [{ code: 0, stderr: 'Interactive authentication required.', stdout: '' }, false],
  ])('isAuthorizationError(%j) = %s', (input, expected) => {
    expect(isAuthorizationError(input)).toBe(expected);
  });

  it('scrubExcerpt removes colour codes and control characters, and caps the length', () => {
    expect(scrubExcerpt('\u001b[1;31mfailed\u001b[0m\u0007 ok\n')).toBe('failed ok');
    const long = scrubExcerpt('x'.repeat(10_000));
    expect(long.length).toBeLessThan(4100);
    expect(long.endsWith('...')).toBe(true);
  });

  it('resolveStartBinaries: the test hooks work only when overrides are allowed, and only for absolute paths', () => {
    const env = { LOCALAI_DESKTOP_SYSTEMCTL: '/tmp/stub-systemctl', LOCALAI_DESKTOP_PKEXEC: '/tmp/stub-pkexec' };
    expect(resolveStartBinaries(env, false)).toEqual({ systemctl: SYSTEMCTL, pkexec: PKEXEC });
    expect(resolveStartBinaries(env, true)).toEqual({ systemctl: '/tmp/stub-systemctl', pkexec: '/tmp/stub-pkexec' });
    expect(resolveStartBinaries({ LOCALAI_DESKTOP_SYSTEMCTL: '/tmp/stub-systemctl' }, true)).toEqual({ systemctl: '/tmp/stub-systemctl', pkexec: null });
    expect(resolveStartBinaries({ LOCALAI_DESKTOP_SYSTEMCTL: 'relative/systemctl' }, true)).toEqual({ systemctl: SYSTEMCTL, pkexec: PKEXEC });
    expect(resolveStartBinaries({}, true)).toEqual({ systemctl: SYSTEMCTL, pkexec: PKEXEC });
  });
});

describe('one start at a time', () => {
  it('a second start while one runs does nothing and says so; afterwards a new start works', async () => {
    const h = harness({ healthyAtPoll: 1 });
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => (release = r));
    const supervisor = createAgentSupervisor({ start: { ...h.deps, exec: async (...args) => (await gate, h.deps.exec(...args)) } });
    const first = supervisor.start!();
    expect(await supervisor.start!()).toEqual({ ok: false, busy: true });
    release();
    expect(await first).toEqual({ ok: true });
    expect(await supervisor.start!()).toEqual({ ok: true });
  });

  it('a start that throws frees the lock', async () => {
    const h = harness({});
    let boom = true;
    const supervisor = createAgentSupervisor({
      start: {
        ...h.deps,
        health: async () => {
          if (boom) throw new Error('boom');
          return true;
        },
      },
    });
    await expect(supervisor.start!()).rejects.toThrow('boom');
    boom = false;
    expect(await supervisor.start!()).toEqual({ ok: true });
  });

  it('a supervisor built without the start pieces has no start', () => {
    expect(createAgentSupervisor().start).toBeUndefined();
  });
});

describe('execFileRunner (the production exec, driven with this Node, never systemctl)', () => {
  it('returns stdout, stderr and the exit status of a failing program', async () => {
    const out = await execFileRunner(process.execPath, ['-e', "process.stdout.write('out'); process.stderr.write('err'); process.exit(126)"], { timeoutMs: 10_000 });
    expect(out).toEqual({ code: 126, stdout: 'out', stderr: 'err', timedOut: false });
  });

  it('reports a missing program and a program that ran past its timeout', async () => {
    expect((await execFileRunner('/nonexistent/program', [], { timeoutMs: 1000 })).code).toBe('ENOENT');
    const slow = await execFileRunner(process.execPath, ['-e', 'setTimeout(() => {}, 5000)'], { timeoutMs: 200 });
    expect(slow.timedOut).toBe(true);
  });

  it('passes no shell: metacharacters in an argument stay literal', async () => {
    const out = await execFileRunner(process.execPath, ['-p', 'process.argv[1]', '$(echo hi); echo no'], { timeoutMs: 10_000 });
    expect(out.stdout.trim()).toBe('$(echo hi); echo no');
  });
});

describe('agent-supervisor.ts source rules (desktop.md 4.2: no shell, fixed arguments)', () => {
  const source = readFileSync(resolve(import.meta.dirname, '../../src/main/agent-supervisor.ts'), 'utf8');

  it('never asks for a shell', () => {
    expect(source).not.toMatch(/shell\s*:\s*true/);
    expect(source).not.toMatch(/\bshell\s*:/);
  });

  it('imports only execFile from child_process (no exec, spawn or execSync)', () => {
    const imports = [...source.matchAll(/import\s*\{([^}]*)\}\s*from\s*'node:child_process'/g)].map((m) => m[1]!.trim());
    expect(imports).toEqual(['execFile']);
    expect(source).not.toMatch(/\b(?:execSync|spawn|spawnSync|execFileSync)\(/);
  });
});
