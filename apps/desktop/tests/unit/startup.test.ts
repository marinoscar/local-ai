import { describe, expect, it, vi } from 'vitest';
import type { HealthResult, SessionResult, StatusResult, TokenResult } from '../../src/main/agent-client';
import type { DskCode } from '../../src/main/i18n';
import {
  type ActionDeps,
  type ContentHost,
  type StartupClient,
  RECOVERY_WINDOW_MS,
  SessionRecovery,
  handleErrorAction,
  isErrorPageUrl,
  isRecoverable401,
  parseActionRequest,
  runStartup,
} from '../../src/main/startup';

const healthy = (version = '0.1.0', starting = false): HealthResult => ({ reachable: true, health: { ok: true, version, mode: 'online', starting } });
const okToken: TokenResult = { ok: true, changed: false };
const okStatus = (mode = 'online'): StatusResult => ({ ok: true, status: { mode: { value: mode } } });

interface Script {
  health?: HealthResult[];
  token?: TokenResult[];
  session?: SessionResult[];
  status?: StatusResult;
  group?: 'not-member' | 'needs-relogin' | 'member-active';
}

function setup(script: Script) {
  const next = <T>(list: T[] | undefined, fallback: T): T => (list && list.length > 1 ? list.shift()! : (list?.[0] ?? fallback));
  const calls: string[] = [];
  const client: StartupClient = {
    health: async () => (calls.push('health'), next(script.health, healthy())),
    readToken: async () => (calls.push('readToken'), next(script.token, okToken)),
    createSession: async () => (calls.push('createSession'), next(script.session, { ok: true })),
    recreateSession: async () => (calls.push('recreateSession'), next(script.session, { ok: true })),
    status: async () => (calls.push('status'), script.status ?? okStatus()),
  };
  const shown: Array<{ error?: DskCode; unit?: string; ui?: string }> = [];
  const host: ContentHost = {
    showError: async (code, options) => void shown.push({ error: code, ...(options?.unit !== undefined ? { unit: options.unit } : {}) }),
    showAgentUi: async (path) => void shown.push({ ui: path }),
    reloadAgentUi: () => void calls.push('reload'),
  };
  const survival = { enter: vi.fn() };
  const supervisor = { unitState: async () => 'inactive', diagnoseTokenAccess: async () => ({ kind: script.group ?? ('not-member' as const) }) };
  return { client, host, survival, supervisor, shown, calls };
}
const run = (s: ReturnType<typeof setup>) => runStartup({ client: s.client, host: s.host, supervisor: s.supervisor, survival: s.survival, sleep: async () => undefined });

describe('start-up sequence (desktop.md 4.1)', () => {
  it('healthy agent, good token, session, status: the agent UI at /', async () => {
    const s = setup({});
    expect(await run(s)).toEqual({ screen: 'agent-ui', mode: 'online' });
    expect(s.shown).toEqual([{ ui: '/' }]);
    expect(s.calls).toEqual(['health', 'readToken', 'createSession', 'status']);
    expect(s.survival.enter).not.toHaveBeenCalled();
  });

  it('agent unreachable: LAI-DSK-004 with the unit state, and nothing else is tried', async () => {
    const s = setup({ health: [{ reachable: false, reason: 'refused' }] });
    expect(await run(s)).toEqual({ screen: 'error', code: 'LAI-DSK-004' });
    expect(s.shown).toEqual([{ error: 'LAI-DSK-004', unit: 'inactive' }]);
    expect(s.calls).toEqual(['health']);
  });

  it('waits while the agent says it is still starting, then continues', async () => {
    const s = setup({ health: [healthy('0.1.0', true), healthy('0.1.0', true), healthy()] });
    expect((await run(s)).screen).toBe('agent-ui');
  });

  it('version major mismatch: LAI-DSK-006 and the token is never read', async () => {
    const s = setup({ health: [healthy('1.2.0')] });
    expect(await run(s)).toEqual({ screen: 'error', code: 'LAI-DSK-006' });
    expect(s.calls).toEqual(['health']);
  });

  it.each([
    ['not-member', 'LAI-DSK-001'],
    ['needs-relogin', 'LAI-DSK-002'],
    ['member-active', 'LAI-DSK-001'],
  ] as const)('token EACCES with group %s: %s', async (group, code) => {
    const s = setup({ token: [{ ok: false, reason: 'permission-denied' }], group });
    expect(await run(s)).toEqual({ screen: 'error', code });
    expect(s.calls).not.toContain('createSession');
  });

  it('token file missing: LAI-DSK-003', async () => {
    const s = setup({ token: [{ ok: false, reason: 'missing' }] });
    expect(await run(s)).toEqual({ screen: 'error', code: 'LAI-DSK-003' });
  });

  it('401 on the session: the token is re-read once, a second 401 is LAI-DSK-007', async () => {
    const refused: SessionResult = { ok: false, kind: 'unauthorized', status: 401 };
    const s = setup({ session: [refused, refused] });
    expect(await run(s)).toEqual({ screen: 'error', code: 'LAI-DSK-007' });
    expect(s.calls.filter((c) => c === 'readToken')).toHaveLength(2);
    expect(s.calls.filter((c) => c === 'createSession')).toHaveLength(2);
  });

  it('401 then success after the re-read (a rotated token) goes on to the agent UI', async () => {
    const s = setup({ session: [{ ok: false, kind: 'unauthorized', status: 401 }, { ok: true }] });
    expect((await run(s)).screen).toBe('agent-ui');
  });

  it('the agent dying during the session request is LAI-DSK-004', async () => {
    const s = setup({ session: [{ ok: false, kind: 'unreachable' }] });
    expect(await run(s)).toEqual({ screen: 'error', code: 'LAI-DSK-004' });
  });

  it('survival mode: the survival hook is called and the Portal is loaded', async () => {
    const s = setup({ status: okStatus('survival') });
    expect(await run(s)).toEqual({ screen: 'survival' });
    expect(s.survival.enter).toHaveBeenCalledTimes(1);
    expect(s.shown).toEqual([{ ui: '/portal' }]);
  });

  describe('survival mode with a token file the desktop cannot use (desktop.md 4.7 rule 5)', () => {
    const inSurvival = healthy();
    inSurvival.reachable && (inSurvival.health.mode = 'survival');

    it.each([
      ['permission-denied', 'not-member', 'LAI-DSK-001'],
      ['permission-denied', 'needs-relogin', 'LAI-DSK-002'],
      ['missing', 'not-member', 'LAI-DSK-003'],
      ['empty', 'not-member', 'LAI-DSK-003'],
    ] as const)('%s / %s: the Portal loads with the code as a banner, not the error page', async (reason, group, code) => {
      const s = setup({ health: [inSurvival], token: [{ ok: false, reason }], group });
      expect(await run(s)).toEqual({ screen: 'survival', banner: code });
      expect(s.survival.enter).toHaveBeenCalledWith({ banner: code });
      expect(s.shown).toEqual([{ ui: '/portal' }]);
      expect(s.calls).toEqual(['health', 'readToken']);
    });

    it('a token that goes bad at the session step is treated the same', async () => {
      const s = setup({ health: [inSurvival], session: [{ ok: false, kind: 'token', token: { ok: false, reason: 'permission-denied' } }] });
      expect(await run(s)).toEqual({ screen: 'survival', banner: 'LAI-DSK-001' });
    });

    it('the same token failure outside survival mode is still the error page', async () => {
      const s = setup({ token: [{ ok: false, reason: 'permission-denied' }] });
      expect(await run(s)).toEqual({ screen: 'error', code: 'LAI-DSK-001' });
      expect(s.survival.enter).not.toHaveBeenCalled();
    });

    it('failures that are not about the token stay error pages (agent down, version, refused session)', async () => {
      const refused = setup({ health: [inSurvival], session: [{ ok: false, kind: 'unauthorized' }, { ok: false, kind: 'unauthorized' }] });
      expect(await run(refused)).toEqual({ screen: 'error', code: 'LAI-DSK-007' });
      const old = healthy('9.0.0');
      old.reachable && (old.health.mode = 'survival');
      const version = setup({ health: [old] });
      expect(await run(version)).toEqual({ screen: 'error', code: 'LAI-DSK-006' });
    });
  });

  it('a status that cannot be read still opens the agent UI', async () => {
    const s = setup({ status: { ok: false, kind: 'unexpected', status: 500 } });
    expect(await run(s)).toEqual({ screen: 'agent-ui', mode: null });
  });
});

describe('401 recovery (desktop.md 4.1)', () => {
  const build = (over: { session?: SessionResult[]; active?: () => boolean } = {}) => {
    let clock = 1_000_000;
    const calls: string[] = [];
    const shown: DskCode[] = [];
    const results = over.session ?? [{ ok: true } as SessionResult];
    const recovery = new SessionRecovery({
      client: { recreateSession: async () => (calls.push('recreate'), results.length > 1 ? results.shift()! : results[0]!) },
      supervisor: { unitState: async () => 'active', diagnoseTokenAccess: async () => ({ kind: 'not-member' }) },
      host: { showError: async (code) => void shown.push(code), reloadAgentUi: () => void calls.push('reload') },
      active: over.active ?? (() => true),
      now: () => clock,
    });
    return { recovery, calls, shown, advance: (ms: number) => (clock += ms) };
  };
  const url = 'http://127.0.0.1:7070/api/v1/status';

  it('re-creates the session and reloads the view', async () => {
    const t = build();
    expect(await t.recovery.onUnauthorized(url)).toBe('recovered');
    expect(t.calls).toEqual(['recreate', 'reload']);
  });

  it('a second 401 within 30 s does not loop; after 30 s it may run again', async () => {
    const t = build();
    await t.recovery.onUnauthorized(url);
    t.advance(RECOVERY_WINDOW_MS - 1);
    expect(await t.recovery.onUnauthorized(url)).toBe('skipped');
    expect(t.calls).toEqual(['recreate', 'reload']);
    t.advance(2);
    expect(await t.recovery.onUnauthorized(url)).toBe('recovered');
    expect(t.calls.filter((c) => c === 'recreate')).toHaveLength(2);
  });

  it('concurrent 401s run one recovery', async () => {
    const t = build();
    const [a, b] = await Promise.all([t.recovery.onUnauthorized(url), t.recovery.onUnauthorized(url)]);
    expect([a, b].sort()).toEqual(['recovered', 'skipped']);
  });

  it('a session that is refused again shows LAI-DSK-007 instead of reloading', async () => {
    const t = build({ session: [{ ok: false, kind: 'unauthorized', status: 401 }] });
    expect(await t.recovery.onUnauthorized(url)).toBe('failed');
    expect(t.shown).toEqual(['LAI-DSK-007']);
    expect(t.calls).not.toContain('reload');
  });

  it('does nothing while the view shows the Portal or an error page', async () => {
    const t = build({ active: () => false });
    expect(await t.recovery.onUnauthorized(url)).toBe('skipped');
    expect(t.calls).toEqual([]);
  });

  it('ignores the 401 of logging in itself and of paths outside the API', () => {
    expect(isRecoverable401('http://127.0.0.1:7070/api/v1/status')).toBe(true);
    expect(isRecoverable401('http://127.0.0.1:7070/api/v1/models/x/load')).toBe(true);
    expect(isRecoverable401('http://127.0.0.1:7070/api/v1/session')).toBe(false);
    expect(isRecoverable401('http://127.0.0.1:7070/api/v1/session/code')).toBe(false);
    expect(isRecoverable401('http://127.0.0.1:7070/api/v1/health')).toBe(false);
    expect(isRecoverable401('http://127.0.0.1:7070/chat/')).toBe(false);
    expect(isRecoverable401('not a url')).toBe(false);
  });
});

describe('error-page actions', () => {
  const deps = (over: Partial<ActionDeps> = {}) => {
    const log = { copied: [] as string[], portal: 0, browser: 0, retried: 0 };
    const d: ActionDeps = {
      host: { showAgentUi: async (path) => void (path === '/portal' && (log.portal += 1)) },
      supervisor: {},
      clipboardWrite: (text) => void log.copied.push(text),
      logout: async () => true,
      openAgentInBrowser: async () => void (log.browser += 1),
      retry: () => void (log.retried += 1),
      ...over,
    };
    return { d, log };
  };

  it('copy-command copies the catalog command of the code, never text from the page', async () => {
    const { d, log } = deps();
    expect(await handleErrorAction({ action: 'copy-command', code: 'LAI-DSK-001' }, d)).toEqual({ ok: true });
    expect(log.copied).toEqual(['sudo usermod -aG localai-operators $USER']);
  });

  it('open-portal loads the Portal in the agent view (no token needed)', async () => {
    const { d, log } = deps();
    expect(await handleErrorAction({ action: 'open-portal', code: 'LAI-DSK-004' }, d)).toEqual({ ok: true });
    expect(log.portal).toBe(1);
  });

  it('start-agent without DSK-05 answers LAI-DSK-005; with it, success retries the sequence', async () => {
    const without = deps();
    expect(await handleErrorAction({ action: 'start-agent', code: 'LAI-DSK-004' }, without.d)).toEqual({ ok: false, code: 'LAI-DSK-005' });
    const withStart = deps({ supervisor: { start: async () => ({ ok: true }) } });
    expect(await handleErrorAction({ action: 'start-agent', code: 'LAI-DSK-004' }, withStart.d)).toEqual({ ok: true });
    expect(withStart.log.retried).toBe(1);
  });

  it('start-agent failure carries LAI-DSK-005 and the status excerpt; a busy start does nothing and does not retry', async () => {
    const failed = deps({ supervisor: { start: async () => ({ ok: false, code: 'LAI-DSK-005', reason: 'not-authorized', statusExcerpt: 'inactive (dead)' }) } });
    expect(await handleErrorAction({ action: 'start-agent', code: 'LAI-DSK-004' }, failed.d)).toEqual({ ok: false, code: 'LAI-DSK-005', statusExcerpt: 'inactive (dead)' });
    expect(failed.log.retried).toBe(0);
    const busy = deps({ supervisor: { start: async () => ({ ok: false, busy: true }) } });
    expect(await handleErrorAction({ action: 'start-agent', code: 'LAI-DSK-004' }, busy.d)).toEqual({ ok: false, busy: true });
    expect(busy.log.retried).toBe(0);
  });

  it('logout reports whether gnome-session-quit was there', async () => {
    expect(await handleErrorAction({ action: 'logout', code: 'LAI-DSK-002' }, deps({ logout: async () => false }).d)).toEqual({ ok: false });
  });

  it('open-browser opens the agent UI in the browser', async () => {
    const { d, log } = deps();
    await handleErrorAction({ action: 'open-browser', code: 'LAI-DSK-007' }, d);
    expect(log.browser).toBe(1);
  });

  it('accepts only an action its own code has', () => {
    expect(parseActionRequest({ action: 'copy-command', code: 'LAI-DSK-001' })).toEqual({ action: 'copy-command', code: 'LAI-DSK-001' });
    expect(parseActionRequest({ action: 'start-agent', code: 'LAI-DSK-001' })).toBeNull();
    expect(parseActionRequest({ action: 'rm -rf', code: 'LAI-DSK-001' })).toBeNull();
    expect(parseActionRequest({ action: 'open-portal', code: 'not-a-code' })).toBeNull();
    expect(parseActionRequest(null)).toBeNull();
    expect(parseActionRequest('open-portal')).toBeNull();
  });

  it('only the error page may call them', () => {
    expect(isErrorPageUrl('localai://app/error/?code=LAI-DSK-001')).toBe(true);
    expect(isErrorPageUrl('localai://app/topbar/')).toBe(false);
    expect(isErrorPageUrl('http://127.0.0.1:7070/')).toBe(false);
    expect(isErrorPageUrl('http://localhost:5173/error/index.html?code=x', 'http://localhost:5173')).toBe(true);
    expect(isErrorPageUrl('http://localhost:5173/error/index.html')).toBe(false);
  });
});
