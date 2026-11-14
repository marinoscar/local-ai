import { describe, expect, it } from 'vitest';
import type { ExecFn, ExecResult, StartBinaries } from '../../src/main/agent-supervisor';
import type { Logger } from '../../src/main/log';
import {
  SYSTEM_ACTION_NAMES,
  SYSTEM_ACTION_TIMEOUT_MS,
  createSystemActions,
  interpretSystemAction,
  isSystemActionName,
  systemActionArgv,
} from '../../src/main/system-actions';

// Issue #500 (dashboard.md 3.2, desktop.md 3.5 and 4.2): the System screen names one action; the main process maps it
// to a fixed `pkexec aiagent ctl …` argv. Nothing here runs a real pkexec: every call goes through a fake `exec`.

const REAL: StartBinaries = { systemctl: '/usr/bin/systemctl', pkexec: '/usr/bin/pkexec' };
const result = (over: Partial<ExecResult> = {}): ExecResult => ({ code: 0, stdout: '', stderr: '', timedOut: false, ...over });

function fakeExec(answer: () => ExecResult | Promise<ExecResult> = () => result()) {
  const calls: { file: string; args: string[]; timeoutMs: number }[] = [];
  const exec: ExecFn = async (file, args, options) => {
    calls.push({ file, args: [...args], timeoutMs: options.timeoutMs });
    return answer();
  };
  return { exec, calls };
}

function recordingLog() {
  const lines: string[] = [];
  const log: Logger = {
    write: (e) => void lines.push(JSON.stringify(e)),
    log: (level, component, msg, fields) => void lines.push(JSON.stringify({ level, component, msg, ...fields })),
  };
  return { log, lines };
}

describe('the allowlist and its fixed argv', () => {
  it('has exactly the four names of the System screen', () => {
    expect([...SYSTEM_ACTION_NAMES]).toEqual(['update-agent', 'kit-verify', 'backup-config', 'restore-config']);
  });

  it('maps each name to one fixed argv (global flags before the command name)', () => {
    const argv = (name: (typeof SYSTEM_ACTION_NAMES)[number], lang: 'en' | 'es' = 'en') => {
      const a = systemActionArgv('/usr/bin/pkexec', name, lang);
      return [a.file, ...a.args];
    };
    const ctl = ['/usr/bin/pkexec', '/opt/localai/bin/aiagent', 'ctl'];
    expect(argv('update-agent')).toEqual([...ctl, '--json', '--lang', 'en', 'update', '--yes']);
    expect(argv('kit-verify', 'es')).toEqual([...ctl, '--lang', 'es', 'kit', 'verify']);
    expect(argv('backup-config')).toEqual([...ctl, '--json', '--lang', 'en', 'backup-config', '--to-ark', '--yes']);
    expect(argv('restore-config')).toEqual([...ctl, '--json', '--lang', 'en', 'backup-config', '--restore', 'latest', '--yes']);
  });

  it('refuses anything else: other strings, argv-shaped words, non-strings', () => {
    for (const bad of ['pin-set', 'token rotate', '--help', 'update-agent ', 'UPDATE-AGENT', '', '../x', null, undefined, 5, ['update-agent'], { name: 'update-agent' }])
      expect(isSystemActionName(bad), JSON.stringify(bad)).toBe(false);
    for (const ok of SYSTEM_ACTION_NAMES) expect(isSystemActionName(ok)).toBe(true);
  });
});

describe('runNamed', () => {
  it('runs the fixed argv with the time limit of the action, in the language given', async () => {
    const { exec, calls } = fakeExec(() => result({ stdout: '{"status":"updated","from":"0.1.0","to":"0.2.0"}' }));
    const actions = createSystemActions({ binaries: REAL, exec });
    expect(await actions.runNamed('update-agent', 'es')).toEqual({ ok: true, status: 'updated', from: '0.1.0', to: '0.2.0' });
    expect(calls).toEqual([
      {
        file: '/usr/bin/pkexec',
        args: ['/opt/localai/bin/aiagent', 'ctl', '--json', '--lang', 'es', 'update', '--yes'],
        timeoutMs: SYSTEM_ACTION_TIMEOUT_MS['update-agent'],
      },
    ]);
  });

  it('an unknown name runs nothing and says so; a language other than es is English', async () => {
    const { exec, calls } = fakeExec();
    const { log, lines } = recordingLog();
    const actions = createSystemActions({ binaries: REAL, exec, log });
    expect(await actions.runNamed('pin-set', 'en')).toEqual({ ok: false, reason: 'unknown-action' });
    expect(await actions.runNamed('/bin/sh', 'en')).toEqual({ ok: false, reason: 'unknown-action' });
    expect(calls).toEqual([]);
    expect(lines.join('\n')).not.toContain('/bin/sh');
    await actions.runNamed('kit-verify', '<x>' as never);
    expect(calls[0]!.args).toEqual(['/opt/localai/bin/aiagent', 'ctl', '--lang', 'en', 'kit', 'verify']);
  });

  it('a build with no pkexec never runs one', async () => {
    const { exec, calls } = fakeExec();
    const actions = createSystemActions({ binaries: { systemctl: '/tmp/stub', pkexec: null }, exec });
    expect(await actions.runNamed('kit-verify', 'en')).toEqual({ ok: false, reason: 'unavailable' });
    expect(calls).toEqual([]);
  });

  it('shares the one-at-a-time lock with the error-page buttons', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const { exec, calls } = fakeExec(async () => {
      await gate;
      return result();
    });
    const actions = createSystemActions({ binaries: REAL, exec, username: () => 'person_a' });
    const first = actions.runNamed('kit-verify', 'en');
    expect(await actions.runNamed('backup-config', 'en')).toEqual({ ok: false, reason: 'busy' });
    expect(await actions.rotateToken()).toEqual({ ok: false, reason: 'busy' });
    release();
    expect(await first).toEqual({ ok: true, overall: 'green' });
    expect(calls).toHaveLength(1);
  });

  it('logs the action and the outcome, never the output', async () => {
    const { exec } = fakeExec(() =>
      result({ code: 5, stdout: '{"error":{"code":"LAI-ARK-001","message":"No AI-ARK drive /media/person_a/AI-ARK","fix":"Connect it."}}' }),
    );
    const { log, lines } = recordingLog();
    const actions = createSystemActions({ binaries: REAL, exec, log });
    await actions.runNamed('backup-config', 'en');
    const text = lines.join('\n');
    expect(text).toContain('backup-config');
    expect(text).toContain('LAI-ARK-001');
    expect(text).not.toContain('person_a');
  });
});

describe('interpretSystemAction', () => {
  it('pkexec 126/127 is cancelled, ENOENT unavailable, a timeout timeout', () => {
    expect(interpretSystemAction('update-agent', result({ code: 126 }))).toEqual({ ok: false, reason: 'cancelled' });
    expect(interpretSystemAction('kit-verify', result({ code: 127 }))).toEqual({ ok: false, reason: 'cancelled' });
    expect(interpretSystemAction('backup-config', result({ code: 'ENOENT' }))).toEqual({ ok: false, reason: 'unavailable' });
    expect(interpretSystemAction('update-agent', result({ code: null, timedOut: true }))).toEqual({ ok: false, reason: 'timeout' });
  });

  it('a failure carries the error envelope: code, message and fix', () => {
    const stdout = '{"error":{"code":"LAI-AGT-023","message":"Updating needs the internet","fix":"Switch to online first.","details":{}}}';
    expect(interpretSystemAction('update-agent', result({ code: 6, stdout }))).toEqual({
      ok: false,
      reason: 'failed',
      code: 'LAI-AGT-023',
      message: 'Updating needs the internet',
      fix: 'Switch to online first.',
    });
    expect(interpretSystemAction('restore-config', result({ code: 1, stdout: 'not json', stderr: 'x [LAI-CLI-012]' }))).toEqual({
      ok: false,
      reason: 'failed',
      code: 'LAI-CLI-012',
    });
    expect(interpretSystemAction('restore-config', result({ code: 1 }))).toEqual({ ok: false, reason: 'failed' });
  });

  it('the kit check is read from its exit code (aictl.md 4.23c)', () => {
    expect(interpretSystemAction('kit-verify', result({ code: 0 }))).toEqual({ ok: true, overall: 'green' });
    expect(interpretSystemAction('kit-verify', result({ code: 8, stdout: 'llama  warning  [LAI-KIT-041]' }))).toEqual({ ok: true, overall: 'yellow', code: 'LAI-KIT-041' });
    expect(interpretSystemAction('kit-verify', result({ code: 9 }))).toEqual({ ok: true, overall: 'red' });
    expect(interpretSystemAction('kit-verify', result({ code: 1, stdout: 'bin damaged [LAI-KIT-001]' }))).toEqual({ ok: true, overall: 'red', code: 'LAI-KIT-001' });
    expect(interpretSystemAction('kit-verify', result({ code: 5, stderr: 'No kit [LAI-KIT-040]' }))).toEqual({ ok: false, reason: 'failed', code: 'LAI-KIT-040' });
  });

  it('a success keeps only the fields the screen words its sentence from', () => {
    expect(interpretSystemAction('update-agent', result({ stdout: '{"status":"already_current","from":"0.1.0","to":"0.1.0","reason":"same_version"}' }))).toEqual({
      ok: true,
      status: 'already_current',
      from: '0.1.0',
      to: '0.1.0',
    });
    expect(
      interpretSystemAction('backup-config', result({ stdout: '{"path":"/media/person_a/AI-ARK/kit/config/ai-n3/x.tar.gz","node_id":"ai-n3","files":14,"secrets_included":true}' })),
    ).toEqual({ ok: true, files: 14, secretsIncluded: true });
    expect(interpretSystemAction('restore-config', result({ stdout: '{"restored":3,"kept":3,"restarted":true}' }))).toEqual({ ok: true, restored: 3, restarted: true });
    expect(interpretSystemAction('backup-config', result({ stdout: '' }))).toEqual({ ok: true });
  });
});
