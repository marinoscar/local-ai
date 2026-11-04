import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { HealthResult, StatusResult, TokenResult } from '../../src/main/agent-client';
import { type ApparmorProbe, type DiagnoseDeps, buildReport, egressTotalFromLog, headlessArgs, needsHeadlessReexec, sandboxBlocked } from '../../src/main/diagnose';
import { DEFAULT_SETTINGS } from '../../src/main/settings';

const KEYS = ['version', 'electron', 'agent', 'token', 'session', 'tray', 'safe_storage_backend', 'cloud', 'egress_blocked_total', 'autostart', 'checks'];

const sandboxOk: ApparmorProbe = { applies: true, restrictUserns: () => '0', loadedProfiles: () => null, profileFilePresent: () => true };

function deps(over: { health?: HealthResult; token?: TokenResult; status?: StatusResult; group?: 'not-member' | 'needs-relogin' | 'member-active'; apparmor?: ApparmorProbe; lang?: 'en' | 'es' } = {}): DiagnoseDeps {
  return {
    version: '0.1.0',
    electron: '44.10.2',
    settings: { ...DEFAULT_SETTINGS },
    lang: over.lang ?? 'en',
    client: {
      health: async () => over.health ?? { reachable: true, health: { ok: true, version: '0.1.0', mode: 'online', starting: false } },
      readToken: async () => over.token ?? { ok: true, changed: false },
      status: async () => over.status ?? { ok: true, status: {} },
    },
    supervisor: { unitState: async () => 'active', diagnoseTokenAccess: async () => ({ kind: over.group ?? 'member-active' }) },
    safeStorageBackend: () => 'basic_text',
    tray: () => 'unknown',
    egressBlockedTotal: () => 3,
    autostartFilePresent: () => false,
    apparmor: over.apparmor ?? sandboxOk,
  };
}

describe('--diagnose report', () => {
  it('has the documented keys and no checks when everything is fine', async () => {
    const report = await buildReport(deps());
    expect(Object.keys(report)).toEqual(KEYS);
    expect(report).toMatchObject({
      version: '0.1.0',
      electron: '44.10.2',
      agent: { reachable: true, version: '0.1.0', unit_state: 'active' },
      token: { readable: true, group_member: true, needs_relogin: false },
      session: 'ok',
      safe_storage_backend: 'basic_text',
      cloud: { linked: false, reachable: null },
      egress_blocked_total: 3,
      autostart: { enabled: true, file_present: false },
      checks: [],
    });
    expect(JSON.parse(JSON.stringify(report))).toEqual(report);
  });

  it('agent down: LAI-DSK-004, session skipped, the rest still reported', async () => {
    const report = await buildReport(deps({ health: { reachable: false, reason: 'refused' } }));
    expect(report.agent.reachable).toBe(false);
    expect(report.session).toBe('skipped');
    expect(report.checks.map((c) => c.code)).toEqual(['LAI-DSK-004']);
    expect(report.checks[0]).toEqual({ code: 'LAI-DSK-004', severity: 'error', title: 'The Local AI agent is not running', fix: 'Press "Start agent" and enter the administrator password.' });
  });

  it('every check has code, severity, title and fix; the command is part of the fix text', async () => {
    const report = await buildReport(deps({ token: { ok: false, reason: 'permission-denied' }, group: 'not-member' }));
    expect(report.token).toEqual({ readable: false, group_member: false, needs_relogin: false });
    expect(report.checks).toHaveLength(1);
    expect(Object.keys(report.checks[0]!)).toEqual(['code', 'severity', 'title', 'fix']);
    expect(report.checks[0]).toMatchObject({ code: 'LAI-DSK-001', severity: 'error' });
    expect(report.checks[0]!.fix).toContain('sudo usermod -aG localai-operators $USER');
  });

  it('listed but not logged in again: LAI-DSK-002 and needs_relogin', async () => {
    const report = await buildReport(deps({ token: { ok: false, reason: 'permission-denied' }, group: 'needs-relogin' }));
    expect(report.token).toEqual({ readable: false, group_member: true, needs_relogin: true });
    expect(report.checks.map((c) => c.code)).toEqual(['LAI-DSK-002']);
    expect(report.checks[0]!.severity).toBe('warning');
  });

  it('missing token file: LAI-DSK-003; version mismatch: LAI-DSK-006; refused token: LAI-DSK-007', async () => {
    expect((await buildReport(deps({ token: { ok: false, reason: 'missing' } }))).checks.map((c) => c.code)).toEqual(['LAI-DSK-003']);
    const mismatch = await buildReport(deps({ health: { reachable: true, health: { ok: true, version: '2.0.0', mode: 'online', starting: false } } }));
    expect(mismatch.checks.map((c) => c.code)).toEqual(['LAI-DSK-006']);
    expect(mismatch.session).toBe('skipped');
    const refused = await buildReport(deps({ status: { ok: false, kind: 'unauthorized', status: 401 } }));
    expect(refused.session).toBe('refused');
    expect(refused.checks.map((c) => c.code)).toEqual(['LAI-DSK-007']);
  });

  it('prints the checks in Spanish when the language is Spanish', async () => {
    const report = await buildReport(deps({ lang: 'es', health: { reachable: false, reason: 'refused' } }));
    expect(report.checks[0]!.title).toBe('El agente de Local AI no está funcionando');
  });

  it('never contains a token', async () => {
    const text = JSON.stringify(await buildReport(deps()));
    expect(text).not.toMatch(/lat_/);
  });
});

describe('LAI-DSK-015 (AppArmor)', () => {
  const probe = (over: Partial<ApparmorProbe>): ApparmorProbe => ({ ...sandboxOk, ...over });
  it('is raised when userns is restricted and the profile is not loaded', () => {
    expect(sandboxBlocked(probe({ restrictUserns: () => '1', loadedProfiles: () => 'snap.foo (enforce)\n' }))).toBe(true);
    expect(sandboxBlocked(probe({ restrictUserns: () => '1\n', loadedProfiles: () => null, profileFilePresent: () => false }))).toBe(true);
  });
  it('is not raised when the profile is loaded, the restriction is off, or this is a development run', () => {
    expect(sandboxBlocked(probe({ restrictUserns: () => '1', loadedProfiles: () => 'ai-desktop (unconfined)\nother (enforce)\n' }))).toBe(false);
    expect(sandboxBlocked(probe({ restrictUserns: () => '1', loadedProfiles: () => null, profileFilePresent: () => true }))).toBe(false);
    expect(sandboxBlocked(probe({ restrictUserns: () => '0' }))).toBe(false);
    expect(sandboxBlocked(probe({ restrictUserns: () => null }))).toBe(false);
    expect(sandboxBlocked(probe({ applies: false, restrictUserns: () => '1', loadedProfiles: () => '' }))).toBe(false);
  });
  it('shows up as a critical check', async () => {
    const report = await buildReport(deps({ apparmor: probe({ restrictUserns: () => '1', loadedProfiles: () => '' }) }));
    expect(report.checks).toEqual([expect.objectContaining({ code: 'LAI-DSK-015', severity: 'critical' })]);
  });
});

describe('headless start', () => {
  it('re-executes without a display on Linux, once', () => {
    expect(needsHeadlessReexec({}, 'linux')).toBe(true);
    expect(needsHeadlessReexec({ DISPLAY: ':0' }, 'linux')).toBe(false);
    expect(needsHeadlessReexec({ WAYLAND_DISPLAY: 'wayland-0' }, 'linux')).toBe(false);
    expect(needsHeadlessReexec({ LOCALAI_DESKTOP_DIAGNOSE_CHILD: '1' }, 'linux')).toBe(false);
    expect(needsHeadlessReexec({}, 'darwin')).toBe(false);
  });
  it('puts the Chromium switches before the application arguments', () => {
    expect(headlessArgs(['/opt/ai-desktop/ai-desktop', '--diagnose'])).toEqual(['--ozone-platform=headless', '--disable-gpu', '--diagnose']);
    expect(headlessArgs(['electron', '/app', '--diagnose'])).toEqual(['--ozone-platform=headless', '--disable-gpu', '/app', '--diagnose']);
  });
});

describe('egress count from the log', () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(join(tmpdir(), 'ai-desktop-diag-'));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });
  it('is the last blocked_total of an egress line, 0 without one or without a log', async () => {
    const file = join(dir, 'main.log');
    expect(egressTotalFromLog(file)).toBe(0);
    await writeFile(file, ['{"component":"agent","msg":"x"}', '{"component":"egress","blocked_total":1}', '{"component":"egress","blocked_total":2}', '{"component":"agent","msg":"y"}', ''].join('\n'));
    expect(egressTotalFromLog(file)).toBe(2);
    await writeFile(file, '{"component":"agent"}\n');
    expect(egressTotalFromLog(file)).toBe(0);
  });
});
