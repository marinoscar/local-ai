import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import type { ExecFn, ExecResult, StartBinaries } from '../../src/main/agent-supervisor';
import type { Logger } from '../../src/main/log';
import {
  AIAGENT,
  CHMOD,
  ETC_DIR,
  LIBRARY_URL,
  USERMOD,
  allowUserArgv,
  createSystemActions,
  isSafeUsername,
  rebootArgv,
  repairAccessArgv,
  rotateTokenArgv,
} from '../../src/main/system-actions';

// desktop.md 4.2 "Buttons instead of commands": every privileged call is one fixed argv. Nothing here touches the real
// system: every call goes through a fake `exec`.

const REAL: StartBinaries = { systemctl: '/usr/bin/systemctl', pkexec: '/usr/bin/pkexec' };
const result = (over: Partial<ExecResult> = {}): ExecResult => ({ code: 0, stdout: '', stderr: '', timedOut: false, ...over });

function fakeExec(answer: (file: string, args: readonly string[]) => ExecResult | Promise<ExecResult> = () => result()) {
  const calls: { file: string; args: string[]; timeoutMs: number }[] = [];
  const exec: ExecFn = async (file, args, options) => {
    calls.push({ file, args: [...args], timeoutMs: options.timeoutMs });
    return answer(file, args);
  };
  return { exec, calls };
}

function recordingLog() {
  const lines: string[] = [];
  const log: Logger = { write: (e) => void lines.push(JSON.stringify(e)), log: (level, component, msg, fields) => void lines.push(JSON.stringify({ level, component, msg, ...fields })) };
  return { log, lines };
}

describe('the fixed argv table', () => {
  it('Allow this user: pkexec usermod -aG localai-operators <the OS user name>', () => {
    expect(allowUserArgv('/usr/bin/pkexec', 'person_a')).toEqual({ file: '/usr/bin/pkexec', args: ['/usr/sbin/usermod', '-aG', 'localai-operators', 'person_a'] });
    expect(USERMOD).toBe('/usr/sbin/usermod');
  });

  it('Make a new token: pkexec /opt/localai/bin/aiagent ctl token rotate (no symlink needed)', () => {
    expect(rotateTokenArgv('/usr/bin/pkexec')).toEqual({ file: '/usr/bin/pkexec', args: ['/opt/localai/bin/aiagent', 'ctl', 'token', 'rotate'] });
    expect(AIAGENT).toBe('/opt/localai/bin/aiagent');
  });

  it('Fix access: pkexec chmod o+x /etc/localai (pass through only, never list; issue #483)', () => {
    expect(repairAccessArgv('/usr/bin/pkexec')).toEqual({ file: '/usr/bin/pkexec', args: ['/usr/bin/chmod', 'o+x', '/etc/localai'] });
    expect([CHMOD, ETC_DIR]).toEqual(['/usr/bin/chmod', '/etc/localai']);
  });

  it('Restart the computer: systemctl reboot, without pkexec (logind and polkit decide)', () => {
    expect(rebootArgv('/usr/bin/systemctl')).toEqual({ file: '/usr/bin/systemctl', args: ['reboot'] });
  });

  it('the library is kiwix-serve on loopback', () => {
    expect(LIBRARY_URL).toBe('http://127.0.0.1:8888/library/');
  });

  it('only a plain POSIX user name reaches usermod; root and anything shaped like an option or a shell word are refused', () => {
    for (const ok of ['person_a', 'operator', '_svc', 'user-1']) expect(isSafeUsername(ok), ok).toBe(true);
    for (const bad of ['root', '', '-G', '--help', 'a b', 'a;b', '$(id)', 'Person', 'a/b', 'x'.repeat(33), 'host$', 'ñandu', undefined, 5]) expect(isSafeUsername(bad), String(bad)).toBe(false);
    expect(allowUserArgv('/usr/bin/pkexec', '--help')).toBeNull();
  });
});

describe('createSystemActions', () => {
  it('runs exactly the fixed argv, with the password-window time limit', async () => {
    const { exec, calls } = fakeExec();
    const actions = createSystemActions({ binaries: REAL, exec, username: () => 'person_a' });
    expect(await actions.allowUser()).toEqual({ ok: true });
    expect(await actions.repairAccess()).toEqual({ ok: true });
    expect(await actions.rotateToken()).toEqual({ ok: true });
    expect(await actions.reboot()).toEqual({ ok: true });
    expect(calls.map((c) => [c.file, ...c.args])).toEqual([
      ['/usr/bin/pkexec', '/usr/sbin/usermod', '-aG', 'localai-operators', 'person_a'],
      ['/usr/bin/pkexec', '/usr/bin/chmod', 'o+x', '/etc/localai'],
      ['/usr/bin/pkexec', '/opt/localai/bin/aiagent', 'ctl', 'token', 'rotate'],
      ['/usr/bin/systemctl', 'reboot'],
    ]);
    expect(calls[0]!.timeoutMs).toBe(120_000);
  });

  it('pkexec exit 126 or 127 is "cancelled" (window closed, password refused); other exits "failed"; a missing binary "unavailable"', async () => {
    for (const [code, reason] of [
      [126, 'cancelled'],
      [127, 'cancelled'],
      [1, 'failed'],
      ['ENOENT', 'unavailable'],
    ] as const) {
      const { exec } = fakeExec(() => result({ code }));
      const actions = createSystemActions({ binaries: REAL, exec, username: () => 'person_a' });
      expect(await actions.allowUser(), String(code)).toEqual({ ok: false, reason });
      expect(await actions.rotateToken(), String(code)).toEqual({ ok: false, reason });
      expect(await actions.repairAccess(), String(code)).toEqual({ ok: false, reason });
    }
  });

  it('a build whose test hooks name no pkexec never runs pkexec', async () => {
    const { exec, calls } = fakeExec();
    const actions = createSystemActions({ binaries: { systemctl: '/tmp/stub-systemctl', pkexec: null }, exec, username: () => 'person_a' });
    expect(await actions.allowUser()).toEqual({ ok: false, reason: 'unavailable' });
    expect(await actions.rotateToken()).toEqual({ ok: false, reason: 'unavailable' });
    expect(await actions.repairAccess()).toEqual({ ok: false, reason: 'unavailable' });
    expect(calls).toEqual([]);
    // Restart uses the systemctl stub, never the real one.
    await actions.reboot();
    expect(calls.map((c) => c.file)).toEqual(['/tmp/stub-systemctl']);
  });

  it('a user name that is not plain is refused before anything runs', async () => {
    const { exec, calls } = fakeExec();
    const actions = createSystemActions({ binaries: REAL, exec, username: () => 'root' });
    expect(await actions.allowUser()).toEqual({ ok: false, reason: 'invalid-user' });
    expect(calls).toEqual([]);
  });

  it('one privileged call at a time: a second press while the password window is open does nothing', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const { exec, calls } = fakeExec(async () => {
      await gate;
      return result();
    });
    const actions = createSystemActions({ binaries: REAL, exec, username: () => 'person_a' });
    const first = actions.rotateToken();
    expect(await actions.allowUser()).toEqual({ ok: false, reason: 'busy' });
    release();
    expect(await first).toEqual({ ok: true });
    expect(calls).toHaveLength(1);
    expect(await actions.allowUser()).toEqual({ ok: true });
  });

  it('logs the action and the outcome, never the user name or the output', async () => {
    const { exec } = fakeExec(() => result({ code: 1, stdout: 'lat_secretsecret', stderr: 'usermod: person_a' }));
    const { log, lines } = recordingLog();
    const actions = createSystemActions({ binaries: REAL, exec, username: () => 'person_a', log });
    await actions.allowUser();
    expect(lines.length).toBeGreaterThan(0);
    for (const line of lines) {
      expect(line).not.toContain('person_a');
      expect(line).not.toContain('lat_');
    }
  });
});

describe('system-actions.ts source rules (no shell, fixed arguments)', () => {
  const source = readFileSync(resolve(import.meta.dirname, '../../src/main/system-actions.ts'), 'utf8');

  it('never asks for a shell and never imports child_process itself (it spawns through the injected exec)', () => {
    expect(source).not.toMatch(/\bshell\s*:/);
    expect(source).not.toMatch(/from\s*'node:child_process'/);
    expect(source).not.toMatch(/\b(?:execSync|spawn|spawnSync|execFileSync)\(/);
  });

  it('names no sudo, no sh -c and no bash', () => {
    expect(source).not.toMatch(/['"](?:\/usr)?\/bin\/(?:ba)?sh['"]|['"]-c['"]|['"]sudo['"]/);
  });
});
