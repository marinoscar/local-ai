// The aictl check over a fake probe, and the real probe over fake `aictl` scripts (one per failure).
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { expectContract } from '../../../test/localai/support/doctor-asserts';
import { AictlDoctorCheck, ExecAictlProbe, interpretVersion, type AictlProbe, type AictlProbeResult } from './aictl.check';

const registry = { register: jest.fn() };
const checkOver = (result: AictlProbeResult) => new AictlDoctorCheck(registry as never, { probe: async () => result } satisfies AictlProbe);

describe('localai.aictl (verdicts)', () => {
  it('registers itself and sits within the 5 s probe bound plus the file checks', () => {
    const check = checkOver({ ok: true, version: null });
    check.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(check);
    expect(check.id).toBe('localai.aictl');
    expect(check.timeoutMs).toBeLessThanOrEqual(10_000);
  });

  it('passes and reports the version', async () => {
    const outcome = await checkOver({ ok: true, version: '0.1.0' }).run();
    expect(outcome).toMatchObject({ status: 'pass', data: { version: '0.1.0' } });
    expect(outcome.detail).toContain('0.1.0');
    expectContract(outcome);
  });

  it.each(['not_configured', 'missing', 'not_executable', 'timeout', 'failed', 'bad_output'] as const)('fails with LAI-CLD-033 for %s', async (reason) => {
    const outcome = await checkOver({ ok: false, reason }).run();
    expect(outcome.status).toBe('fail');
    expect(outcome.data).toMatchObject({ code: 'LAI-CLD-033', reason });
    expect(outcome.detail).toContain('LAI-CLD-033');
    expect(outcome.remedy).toContain('laictl deploy update');
    expectContract(outcome);
  });
});

describe('interpretVersion', () => {
  it('reads cli.version', () => {
    expect(interpretVersion(JSON.stringify({ cli: { version: '0.1.0', commit: 'a1b2c3d' }, agent: null }))).toEqual({ ok: true, version: '0.1.0' });
  });
  it('accepts the flat shape a development build prints', () => {
    expect(interpretVersion('{"version":"0.0.0-dev","commit":"unknown"}')).toEqual({ ok: true, version: '0.0.0-dev' });
  });
  it('still accepts a document without a usable version', () => {
    expect(interpretVersion('{}')).toEqual({ ok: true, version: null });
    expect(interpretVersion(JSON.stringify({ cli: { version: 'has spaces and\nlines' } }))).toEqual({ ok: true, version: null });
  });
  it('rejects output that is not a JSON object', () => {
    for (const text of ['', 'not json', '[]', 'null', '12']) expect(interpretVersion(text)).toEqual({ ok: false, reason: 'bad_output' });
  });
});

describe('ExecAictlProbe (fake binaries)', () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'doctor-aictl-spec-'));
  });
  afterEach(() => rmSync(dir, { recursive: true, force: true }));

  const fake = (name: string, body: string, mode = 0o755) => {
    const path = join(dir, name);
    writeFileSync(path, `#!/bin/sh\n${body}\n`);
    chmodSync(path, mode);
    return path;
  };
  const probeAt = (path: string | undefined, timeoutMs = 5000) => new ExecAictlProbe(timeoutMs, path === undefined ? { PATH: '/usr/bin:/bin' } : { PATH: '/usr/bin:/bin', LOCALAI_AICTL_PATH: path }).probe();

  it('runs `--json version` with an argv array and reads the version', async () => {
    const spy = join(dir, 'argv.txt');
    const path = fake('aictl', `echo "$@" > '${spy}'\necho '{"cli":{"version":"0.1.0"},"agent":null}'`);
    expect(await probeAt(path)).toEqual({ ok: true, version: '0.1.0' });
    expect(require('node:fs').readFileSync(spy, 'utf8').trim()).toBe('--json version');
  });

  it('passes the child nothing but PATH', async () => {
    const path = fake('aictl', `if [ -n "$SECRETS_ENCRYPTION_KEY$JWT_SECRET" ]; then exit 9; fi\necho '{"cli":{"version":"1.2.3"}}'`);
    process.env.JWT_SECRET = 'not-for-the-child';
    try {
      expect(await probeAt(path)).toEqual({ ok: true, version: '1.2.3' });
    } finally {
      delete process.env.JWT_SECRET;
    }
  });

  it('LOCALAI_AICTL_PATH unset -> not_configured', async () => {
    expect(await probeAt(undefined)).toEqual({ ok: false, reason: 'not_configured' });
  });

  it('a path that does not exist -> missing', async () => {
    expect(await probeAt('/nonexistent/aictl')).toEqual({ ok: false, reason: 'missing' });
  });

  it('a file that is not executable, and a directory -> not_executable', async () => {
    expect(await probeAt(fake('plain', 'exit 0', 0o644))).toEqual({ ok: false, reason: 'not_executable' });
    expect(await probeAt(dir)).toEqual({ ok: false, reason: 'not_executable' });
  });

  it('a non-zero exit -> failed', async () => {
    expect(await probeAt(fake('crash', 'echo boom >&2\nexit 1'))).toEqual({ ok: false, reason: 'failed' });
  });

  it('an answer that is not JSON -> bad_output', async () => {
    expect(await probeAt(fake('weird', 'echo hello'))).toEqual({ ok: false, reason: 'bad_output' });
  });

  it('a binary that does not answer in time -> timeout', async () => {
    const started = Date.now();
    expect(await probeAt(fake('slow', 'sleep 20'), 300)).toEqual({ ok: false, reason: 'timeout' });
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it('an endless flood of output -> bad_output', async () => {
    expect(await probeAt(fake('flood', 'head -c 300000 /dev/zero | tr "\\0" "a"'))).toEqual({ ok: false, reason: 'bad_output' });
  });
});

describeDoctorCheckConformance('localai.aictl', () => checkOver({ ok: true, version: '0.1.0' }), {
  scenarios: [
    { name: 'aictl answers', expectStatus: 'pass' },
    { name: 'aictl is missing', build: () => checkOver({ ok: false, reason: 'missing' }), expectStatus: 'fail' },
  ],
});
