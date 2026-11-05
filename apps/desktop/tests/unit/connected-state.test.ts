// Connected mode (docs/specs/desktop.md 4.8 and 4.9): the truth table, the egress switch and the aborts, the link
// through the session, start-up with a stored token, disconnect, and the URL checks. Everything the session touches is a fake.
import { randomBytes, randomUUID } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import {
  ACTIVATE_URL,
  AbortRegistry,
  CloudRequestError,
  CloudSession,
  LinkWindowController,
  type CloudHttp,
  type CloudHttpInit,
  type CloudHttpResult,
  type LinkWindowLike,
  cloudApiPath,
  createCloudHttp,
  isActivationUrl,
  modeFromEvent,
  modeFromStatus,
  safeDisplayName,
} from '../../src/main/cloud-link';
import type { LoadResult, SaveResult, SecretStore, StoredSecret } from '../../src/main/secret-store';

const random = (prefix: string): string => `${prefix}${randomBytes(24).toString('base64url')}`;
const envelope = (data: unknown): unknown => ({ data, meta: {} });
const rfc = (error: string): CloudHttpResult => ({ status: 400, body: { error } });
const tick = (): Promise<void> => new Promise((resolve) => setImmediate(resolve));
const stored = (over: Partial<StoredSecret> = {}): StoredSecret => ({ token: random('pat_'), tokenId: randomUUID(), expiresAt: '2099-01-01T00:00:00.000Z', ...over });

interface Rig {
  session: CloudSession;
  /** Everything with a side effect, in order. */
  events: string[];
  settings: { linked: boolean };
  http: { calls: Array<{ path: string; method: string; bearer: string | undefined }>; handler: (path: string, init: CloudHttpInit) => CloudHttpResult | Promise<CloudHttpResult> };
  secrets: SecretStore & { saved: StoredSecret | null; file: StoredSecret | 'unreadable' | null; saveResult: SaveResult };
  opened: string[];
  removed: number;
  clock: { now: number };
}

function rig(options: { linked?: boolean; file?: StoredSecret | 'unreadable' | null; saveResult?: SaveResult; mode?: 'online' | 'offline' | 'survival' | null } = {}): Rig {
  const events: string[] = [];
  const settings = { linked: options.linked ?? false };
  const opened: string[] = [];
  const clock = { now: Date.parse('2026-11-05T12:00:00Z') };
  const http: Rig['http'] = {
    calls: [],
    handler: () => {
      throw new Error('no handler');
    },
  };
  const secrets: Rig['secrets'] = {
    saved: null,
    file: options.file ?? null,
    saveResult: options.saveResult ?? { where: 'file' },
    protection: () => 'strong',
    async save(secret) {
      secrets.saved = secret;
      if (secrets.saveResult.where === 'file') secrets.file = secret;
      events.push(`save:${secrets.saveResult.where}`);
      return secrets.saveResult;
    },
    async load(): Promise<LoadResult> {
      if (secrets.file === 'unreadable') return { kind: 'unreadable' };
      return secrets.file ? { kind: 'ok', secret: secrets.file } : { kind: 'none' };
    },
    async clear() {
      secrets.file = null;
      events.push('clear');
    },
  };
  const fakeHttp: CloudHttp = async (path, init) => {
    events.push(`http:${path}`);
    http.calls.push({ path, method: init.method, bearer: init.bearer });
    return http.handler(path, init);
  };
  const out: Rig = {
    events, settings, http, secrets, opened, removed: 0, clock,
    session: new CloudSession({
      http: fakeHttp,
      secrets,
      linked: () => settings.linked,
      setLinked: async (linked) => void ((settings.linked = linked), events.push(`linked:${linked}`)),
      url: () => 'https://ai.marin.cr',
      setEgressConnected: (connected) => events.push(`egress:${connected}`),
      hostname: () => 'host-a',
      version: '0.1.0',
      openExternal: (url) => void opened.push(url),
      now: () => clock.now,
      sleep: async (ms) => void (clock.now += ms),
      onRemoved: () => void (out.removed += 1),
    }),
  };
  if (options.mode !== null) out.session.setModeSignal({ mode: options.mode ?? 'online', leaving: false });
  return out;
}

const grant = (over: Record<string, unknown> = {}): CloudHttpResult => ({
  status: 201,
  body: envelope({ deviceCode: 'dc', userCode: 'ABCD-1234', verificationUri: ACTIVATE_URL, verificationUriComplete: `${ACTIVATE_URL}?code=ABCD-1234`, expiresIn: 900, interval: 5, ...over }),
});
const patAnswer = (secret: StoredSecret): CloudHttpResult => ({
  status: 200,
  body: envelope({ accessToken: secret.token, tokenType: 'Bearer', credentialType: 'pat', expiresAt: secret.expiresAt, tokenId: secret.tokenId }),
});
const me = (displayName: unknown): CloudHttpResult => ({ status: 200, body: envelope({ id: 'u1', email: 'person-a@example.test', displayName }) });

/** The cloud as the link flow sees it: a grant, then the polls in order. */
function cloudScript(r: Rig, polls: CloudHttpResult[], profile: CloudHttpResult = me('Person A'), grantOver: Record<string, unknown> = {}): void {
  let n = 0;
  r.http.handler = (path) => {
    if (path === '/api/auth/device/code') return grant(grantOver);
    if (path === '/api/auth/device/token') return polls[n++] ?? rfc('authorization_pending');
    if (path === '/api/auth/me') return profile;
    return { status: 404, body: null };
  };
}

const settle = async (r: Rig, until: () => boolean): Promise<void> => {
  for (let i = 0; i < 50 && !until(); i++) await tick();
  expect(until()).toBe(true);
};

describe('connectedModeActive: linked x token x mode (desktop.md 4.9)', () => {
  const modes = ['online', 'offline', 'survival', null] as const;
  const cases: Array<[linked: boolean, token: boolean, mode: (typeof modes)[number], leaving: boolean]> = [];
  for (const linked of [true, false]) for (const token of [true, false]) for (const mode of modes) for (const leaving of [false, true]) cases.push([linked, token, mode, leaving]);

  it.each(cases)('linked=%s token=%s mode=%s leaving=%s', async (linked, token, mode, leaving) => {
    const r = rig({ linked, file: token ? stored() : null, mode: null });
    await r.session.start();
    // start() with a token and linked=false adopts it; with no token and linked=true it unlinks. Compare with what holds after.
    r.session.setModeSignal({ mode, leaving });
    const expected = r.settings.linked && r.session.tokenHeld && mode === 'online' && !leaving;
    expect(r.session.connectedModeActive()).toBe(expected);
    // The egress switch is on exactly when connected mode is (no link attempt here).
    const egress = r.events.filter((e) => e.startsWith('egress:')).pop();
    expect(egress === 'egress:true').toBe(expected);
  });

  it('only linked + token + online is active: the one true row', async () => {
    const r = rig({ linked: true, file: stored() });
    await r.session.start();
    expect(r.session.connectedModeActive()).toBe(true);
    expect(r.events).toContain('egress:true');
  });
});

describe('mode signals', () => {
  it.each([
    ['online', { mode: { value: 'online', transition: null } }, { mode: 'online', leaving: false }],
    ['going offline', { mode: { value: 'online', transition: { target: 'offline', step: 'x' } } }, { mode: 'online', leaving: true }],
    ['going to survival', { mode: { value: 'online', transition: { target: 'survival' } } }, { mode: 'online', leaving: true }],
    ['coming back online', { mode: { value: 'offline', transition: { target: 'online' } } }, { mode: 'offline', leaving: false }],
    ['unknown mode', { mode: { value: 'sleeping' } }, { mode: null, leaving: false }],
    ['no mode', {}, { mode: null, leaving: false }],
    ['garbage', 'x', { mode: null, leaving: false }],
  ])('/status %s', (_name, status, expected) => {
    expect(modeFromStatus(status)).toEqual(expected);
  });

  it.each([
    ['mode.changed to offline', { type: 'mode.changed', data: { from: 'online', to: 'offline' } }, { mode: 'offline', leaving: false }],
    ['mode.changed to an unknown mode', { type: 'mode.changed', data: { to: 'x' } }, null],
    ['mode.step started toward offline', { type: 'mode.step', data: { target: 'offline', step: 's', state: 'started' } }, { leaving: true }],
    ['mode.step done toward survival', { type: 'mode.step', data: { target: 'survival', state: 'done' } }, { leaving: true }],
    ['mode.step failed toward offline', { type: 'mode.step', data: { target: 'offline', state: 'failed' } }, null],
    ['mode.step toward online', { type: 'mode.step', data: { target: 'online', state: 'started' } }, null],
    ['another event', { type: 'model.loaded', data: {} }, null],
  ])('event %s', (_name, event, expected) => {
    expect(modeFromEvent(event)).toEqual(expected);
  });
});

describe('leaving online closes the cloud at once (desktop.md 4.9)', () => {
  it('mode.step toward offline: the egress switch goes off BEFORE the aborts, a held request fails in under 1 s, the next is refused with no call', async () => {
    const r = rig({ linked: true, file: stored() });
    await r.session.start();
    expect(r.session.connectedModeActive()).toBe(true);
    await tick(); // the "who is linked" call that start-up makes has failed by now (no handler yet)
    r.http.calls.length = 0;

    const order: string[] = [];
    r.http.handler = (_path, init) =>
      new Promise((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => {
          order.push('aborted');
          reject(new DOMException('aborted', 'AbortError'));
        });
      });
    // Record the egress flip in the same list as the abort, to prove the order.
    const unsubscribe = r.session.onChange(() => order.push(`active:${r.session.connectedModeActive()}`));
    const held = r.session.request({ method: 'GET', path: '/api/localai/nodes' });
    const heldResult = held.then(() => 'resolved', (error: unknown) => (error as CloudRequestError).kind);
    await tick();
    expect(r.http.calls).toHaveLength(1);
    expect(r.http.calls[0]!.bearer).toMatch(/^pat_/);

    const started = performance.now();
    const change = modeFromEvent({ type: 'mode.step', data: { target: 'offline', step: 'first', state: 'started' } });
    r.session.setModeSignal(change!);
    expect(r.events.filter((e) => e.startsWith('egress:')).pop()).toBe('egress:false'); // synchronous: nothing waited on the agent
    expect(await heldResult).toBe('aborted');
    expect(performance.now() - started).toBeLessThan(1000);
    expect(order).toEqual(['aborted', 'active:false']); // the abort listener ran inside setModeSignal, before listeners are told

    // The next request is refused before a socket (here: before the transport) is touched.
    await expect(r.session.request({ method: 'GET', path: '/api/localai/nodes' })).rejects.toMatchObject({ name: 'CloudRequestError', kind: 'offline' });
    expect(r.http.calls).toHaveLength(1);
    unsubscribe();
  });

  it('the status read in the middle of a transition keeps it closed; mode.changed to online opens it again', async () => {
    const r = rig({ linked: true, file: stored() });
    await r.session.start();
    r.session.setModeSignal(modeFromStatus({ mode: { value: 'online', transition: { target: 'survival' } } }));
    expect(r.session.connectedModeActive()).toBe(false);
    r.session.setModeSignal(modeFromEvent({ type: 'mode.changed', data: { from: 'online', to: 'survival' } })!);
    expect(r.session.connectedModeActive()).toBe(false);
    r.session.setModeSignal(modeFromEvent({ type: 'mode.changed', data: { from: 'survival', to: 'online' } })!);
    expect(r.session.connectedModeActive()).toBe(true);
    expect(r.events.filter((e) => e.startsWith('egress:'))).toEqual(['egress:true', 'egress:false', 'egress:true']);
  });

  it('every request in flight is aborted, not only one', async () => {
    const r = rig({ linked: true, file: stored() });
    await r.session.start();
    r.http.handler = (_p, init) => new Promise((_res, rej) => init.signal?.addEventListener('abort', () => rej(new Error('aborted'))));
    const kinds = [1, 2, 3].map(() => r.session.request({ method: 'GET', path: '/api/auth/me' }).then(() => 'ok', (e: CloudRequestError) => e.kind));
    await tick();
    r.session.setModeSignal({ leaving: true });
    expect(await Promise.all(kinds)).toEqual(['aborted', 'aborted', 'aborted']);
  });
});

describe('request', () => {
  it('adds the bearer, returns the cloud JSON, and keeps the token out of the answer', async () => {
    const secret = stored();
    const r = rig({ linked: true, file: secret });
    await r.session.start();
    r.http.handler = (path) => (path === '/api/auth/me' ? me('Person A') : { status: 200, body: envelope({ nodes: [] }) });
    const answer = await r.session.request({ method: 'GET', path: '/api/localai/nodes' });
    expect(answer).toEqual({ ok: true, status: 200, body: envelope({ nodes: [] }) });
    expect(JSON.stringify(answer)).not.toContain(secret.token);
    expect(r.http.calls.at(-1)).toEqual({ path: '/api/localai/nodes', method: 'GET', bearer: secret.token });
  });

  it.each(['https://evil.example/api/x', '//evil.example/api/x', '/api/../admin', '/api/%2e%2e/admin', '/apix', '/api//x', 'api/x', '/api\\x'])('refuses the path %s without a call', async (path) => {
    const r = rig({ linked: true, file: stored() });
    await r.session.start();
    r.http.handler = () => ({ status: 200, body: me('x').valueOf() });
    r.http.calls.length = 0;
    await expect(r.session.request({ method: 'GET', path })).rejects.toMatchObject({ kind: 'bad_request' });
    expect(r.http.calls).toHaveLength(0);
  });

  it('a 401 deletes the token, unlinks and reports LAI-DSK-011 once', async () => {
    const r = rig({ linked: true, file: stored() });
    r.http.handler = (path) => (path === '/api/auth/me' ? { status: 401, body: null } : { status: 401, body: null });
    await r.session.start(); // activation triggers /api/auth/me, which answers 401
    await settle(r, () => r.removed > 0);
    expect(r.removed).toBe(1);
    expect(r.settings.linked).toBe(false);
    expect(r.secrets.file).toBeNull();
    expect(r.session.tokenHeld).toBe(false);
    expect(r.events.filter((e) => e.startsWith('egress:')).pop()).toBe('egress:false');
    await expect(r.session.request({ method: 'GET', path: '/api/localai/nodes' })).rejects.toMatchObject({ kind: 'offline' });
  });

  it('a network failure is "unreachable" and does not unlink', async () => {
    const r = rig({ linked: true, file: stored() });
    r.http.handler = (path) => (path === '/api/auth/me' ? me('Person A') : Promise.reject(new TypeError('fetch failed')));
    await r.session.start();
    await expect(r.session.request({ method: 'GET', path: '/api/localai/nodes' })).rejects.toMatchObject({ kind: 'unreachable' });
    expect(r.settings.linked).toBe(true);
    expect(r.removed).toBe(0);
  });
});

describe('start-up', () => {
  it('a stored token and linked -> token held, no notice', async () => {
    const r = rig({ linked: true, file: stored() });
    r.http.handler = () => me('Person A');
    await r.session.start();
    expect(r.session.tokenHeld).toBe(true);
    expect(r.removed).toBe(0);
    expect(r.session.state()).toMatchObject({ linked: true, tokenHeld: true, expiresAt: '2099-01-01T00:00:00.000Z' });
  });

  it.each([
    ['decrypt failure (the keyring changed)', 'unreadable' as const, true],
    ['linked but the token file is gone', null, true],
    ['a PAT past its expiry', stored({ expiresAt: '2020-01-01T00:00:00.000Z' }), true],
  ])('%s -> unlinked, file removed, LAI-DSK-011', async (_name, file, linked) => {
    const r = rig({ linked, file });
    await r.session.start();
    expect(r.removed).toBe(1);
    expect(r.settings.linked).toBe(false);
    expect(r.secrets.file).toBeNull();
    expect(r.session.tokenHeld).toBe(false);
    expect(r.session.connectedModeActive()).toBe(false);
  });

  it('not linked and no token: nothing happens, no notice', async () => {
    const r = rig();
    await r.session.start();
    expect(r.removed).toBe(0);
    expect(r.events).toEqual([]);
  });

  it('a token file while not linked (a crash after saving) is adopted', async () => {
    const r = rig({ linked: false, file: stored() });
    r.http.handler = () => me('Person A');
    await r.session.start();
    expect(r.settings.linked).toBe(true);
    expect(r.session.tokenHeld).toBe(true);
  });

  it('the cloud is not asked about the user while the node is offline', async () => {
    const r = rig({ linked: true, file: stored(), mode: 'offline' });
    await r.session.start();
    expect(r.http.calls).toHaveLength(0);
    expect(r.events).not.toContain('egress:true');
  });
});

describe('link', () => {
  it('refused while the node is offline, in survival, mid-transition or unknown: no request, no egress', async () => {
    for (const signal of [{ mode: 'offline' as const }, { mode: 'survival' as const }, { mode: 'online' as const, leaving: true }, { mode: null }]) {
      const r = rig({ mode: null });
      r.session.setModeSignal({ leaving: false, ...signal });
      expect(await r.session.beginLink()).toEqual({ ok: false, code: 'not_online' });
      expect(r.http.calls).toHaveLength(0);
      expect(r.events).toEqual([]);
    }
  });

  it('refused when already linked', async () => {
    const r = rig({ linked: true, file: stored() });
    r.http.handler = () => me('Person A');
    await r.session.start();
    expect(await r.session.beginLink()).toEqual({ ok: false, code: 'already_linked' });
  });

  it('success: code shown, polled, PAT stored through the secret store, linked=true, who is linked fetched with the new token', async () => {
    const secret = stored();
    const r = rig();
    cloudScript(r, [rfc('authorization_pending'), rfc('slow_down'), patAnswer(secret)]);
    const begin = await r.session.beginLink();
    expect(begin).toEqual({ userCode: 'ABCD-1234', verificationUri: ACTIVATE_URL, expiresIn: 900 });
    // The cloud is open for the link attempt although nothing is linked yet.
    expect(r.events[0]).toBe('egress:true');
    // The egress switch is on BEFORE the first request leaves (the real guard blocks a request made earlier).
    expect(r.events.indexOf('egress:true')).toBeLessThan(r.events.indexOf('http:/api/auth/device/code'));
    expect(r.session.connectedModeActive()).toBe(false);
    expect(r.session.state().link).toEqual({ phase: 'pending' });
    await settle(r, () => r.session.state().link?.phase === 'done');
    expect(r.secrets.saved).toEqual(secret);
    expect(r.settings.linked).toBe(true);
    expect(r.events.indexOf('save:file')).toBeLessThan(r.events.indexOf('linked:true'));
    expect(r.session.connectedModeActive()).toBe(true);
    await settle(r, () => r.session.state().user === 'Person A');
    expect(r.http.calls.at(-1)).toEqual({ path: '/api/auth/me', method: 'GET', bearer: secret.token });
    // Nothing the page can read holds the token or the device code.
    expect(JSON.stringify(r.session.state())).not.toContain(secret.token);
    expect(JSON.stringify(r.session.state())).not.toContain('dc');
    // The device code went only to the token route; the PAT was never sent anywhere before it existed.
    expect(r.http.calls.filter((c) => c.bearer !== undefined).every((c) => c.path === '/api/auth/me')).toBe(true);
  });

  it('the device request names this computer and asks for a pat', async () => {
    const r = rig();
    const bodies: unknown[] = [];
    r.http.handler = (path, init) => (path === '/api/auth/device/code' ? (bodies.push(init.body), grant()) : rfc('authorization_pending'));
    await r.session.beginLink();
    expect(bodies).toEqual([{ clientInfo: { deviceName: 'ai-desktop on host-a', userAgent: 'ai-desktop/0.1.0', tokenType: 'pat' } }]);
    r.session.cancelLink();
  });

  it('no keyring and no consent: connected for this run only, LAI-DSK-008 in the progress, linked=true', async () => {
    const r = rig({ saveResult: { where: 'memory', reason: 'no-keyring' } });
    cloudScript(r, [patAnswer(stored())]);
    await r.session.beginLink();
    await settle(r, () => r.session.state().link?.phase === 'done');
    expect(r.session.state().link).toEqual({ phase: 'done', memoryOnly: true, code: 'LAI-DSK-008' });
    expect(r.session.tokenHeld).toBe(true);
    expect(r.secrets.file).toBeNull();
    expect(r.session.connectedModeActive()).toBe(true);
  });

  it.each([
    ['denied', rfc('access_denied'), { phase: 'denied', code: 'LAI-DSK-009' }],
    ['expired (the cloud says so)', rfc('expired_token'), { phase: 'expired', code: 'LAI-DSK-009' }],
    ['an unexpected answer', rfc('server_error'), { phase: 'failed' }],
  ])('%s ends the attempt, closes the cloud again and stores nothing', async (_name, answer, progress) => {
    const r = rig();
    cloudScript(r, [answer]);
    await r.session.beginLink();
    await settle(r, () => r.session.state().link?.phase !== 'pending');
    expect(r.session.state().link).toEqual(progress);
    expect(r.session.tokenHeld).toBe(false);
    expect(r.secrets.saved).toBeNull();
    expect(r.settings.linked).toBe(false);
    expect(r.events.filter((e) => e.startsWith('egress:'))).toEqual(['egress:true', 'egress:false']);
  });

  it('the code runs out (expiresIn seconds of polling) -> expired, LAI-DSK-009', async () => {
    const r = rig();
    cloudScript(r, [], me('x'), { expiresIn: 12, interval: 5 });
    await r.session.beginLink();
    await settle(r, () => r.session.state().link?.phase === 'expired');
    expect(r.session.state().link).toEqual({ phase: 'expired', code: 'LAI-DSK-009' });
  });

  it('the cloud cannot be reached for the code -> a refusal "unreachable", the egress switch back off', async () => {
    const r = rig();
    r.http.handler = () => Promise.reject(new TypeError('fetch failed'));
    expect(await r.session.beginLink()).toEqual({ ok: false, code: 'unreachable' });
    expect(r.session.state().link).toEqual({ phase: 'unreachable' });
    expect(r.events.filter((e) => e.startsWith('egress:'))).toEqual(['egress:true', 'egress:false']);
  });

  it('the device flow switched off in the cloud (404) -> "unavailable"', async () => {
    const r = rig();
    r.http.handler = () => ({ status: 404, body: null });
    expect(await r.session.beginLink()).toEqual({ ok: false, code: 'unavailable' });
  });

  it('a second beginLink while one runs returns the same code (two windows, one attempt)', async () => {
    const r = rig();
    cloudScript(r, []);
    const first = await r.session.beginLink();
    const second = await r.session.beginLink();
    expect(second).toMatchObject({ userCode: 'ABCD-1234' });
    expect(first).toMatchObject({ userCode: 'ABCD-1234' });
    expect(r.http.calls.filter((c) => c.path === '/api/auth/device/code')).toHaveLength(1);
    r.session.cancelLink();
  });

  it('closing the window (cancelLink) stops polling, closes the cloud and forgets the attempt', async () => {
    const r = rig();
    cloudScript(r, []);
    await r.session.beginLink();
    r.session.cancelLink();
    await settle(r, () => r.session.state().link?.phase === 'cancelled');
    r.session.cancelLink(); // closing a finished attempt clears it
    expect(r.session.state().link).toBeUndefined();
    expect(r.events.filter((e) => e.startsWith('egress:')).pop()).toBe('egress:false');
    expect(r.settings.linked).toBe(false);
  });

  it('the node leaving online during the attempt cancels it and closes the cloud', async () => {
    const r = rig();
    r.http.handler = (path, init) => (path === '/api/auth/device/code' ? grant() : new Promise((_res, rej) => init.signal?.addEventListener('abort', () => rej(new Error('aborted')))));
    await r.session.beginLink();
    await tick();
    r.session.setModeSignal(modeFromEvent({ type: 'mode.step', data: { target: 'offline', state: 'started' } })!);
    await settle(r, () => r.session.state().link?.phase === 'cancelled');
    expect(r.events.filter((e) => e.startsWith('egress:')).pop()).toBe('egress:false');
  });

  describe('Open the browser', () => {
    it('opens the activation URL with the code', async () => {
      const r = rig();
      cloudScript(r, []);
      expect(r.session.openVerification()).toBe(false); // no attempt yet
      await r.session.beginLink();
      expect(r.session.openVerification()).toBe(true);
      expect(r.opened).toEqual([`${ACTIVATE_URL}?code=ABCD-1234`]);
      r.session.cancelLink();
    });

    it.each(['https://evil.example/activate?code=ABCD-1234', 'http://ai.marin.cr/activate', 'https://ai.marin.cr/other', 'javascript:alert(1)', '', 'https://ai.marin.cr.evil.example/activate'])(
      'a verification URL of %j is never opened; the plain activation page is',
      async (url) => {
        const r = rig();
        cloudScript(r, [], me('x'), { verificationUriComplete: url });
        await r.session.beginLink();
        r.session.openVerification();
        expect(r.opened).toEqual([ACTIVATE_URL]);
        r.session.cancelLink();
      },
    );
  });
});

describe('disconnect', () => {
  it('connected: the PAT is revoked in the cloud (DELETE /api/pat/{id}), then the file, the link and the egress go', async () => {
    const secret = stored();
    const r = rig({ linked: true, file: secret });
    r.http.handler = (path, init) => (init.method === 'DELETE' ? { status: 204, body: null } : me('Person A'));
    await r.session.start();
    await settle(r, () => r.session.state().user === 'Person A');
    expect(await r.session.unlink()).toEqual({ revoked: true });
    expect(r.http.calls.at(-1)).toEqual({ path: `/api/pat/${secret.tokenId}`, method: 'DELETE', bearer: secret.token });
    expect(r.secrets.file).toBeNull();
    expect(r.settings.linked).toBe(false);
    expect(r.session.state()).toEqual({ linked: false, tokenHeld: false, url: 'https://ai.marin.cr' });
    expect(r.events.filter((e) => e.startsWith('egress:')).pop()).toBe('egress:false');
    expect(r.removed).toBe(0); // the user asked: no LAI-DSK-011
  });

  it('the node is offline: no call to the cloud (it is closed), the local side is still cleaned', async () => {
    const r = rig({ linked: true, file: stored(), mode: 'offline' });
    await r.session.start();
    expect(await r.session.unlink()).toEqual({ revoked: false });
    expect(r.http.calls).toHaveLength(0);
    expect(r.secrets.file).toBeNull();
    expect(r.settings.linked).toBe(false);
  });

  it('the cloud cannot be reached: still disconnected locally', async () => {
    const r = rig({ linked: true, file: stored() });
    r.http.handler = (path, init) => (init.method === 'DELETE' ? Promise.reject(new TypeError('fetch failed')) : me('Person A'));
    await r.session.start();
    expect(await r.session.unlink()).toEqual({ revoked: false });
    expect(r.settings.linked).toBe(false);
  });
});

describe('URL and name checks', () => {
  it.each([
    ['https://ai.marin.cr/activate', true],
    ['https://ai.marin.cr/activate?code=ABCD-1234', true],
    ['https://ai.marin.cr/activate/', true],
    ['https://AI.MARIN.CR/activate', true],
    ['http://ai.marin.cr/activate', false],
    ['https://ai.marin.cr:8443/activate', false],
    ['https://ai.marin.cr.evil.example/activate', false],
    ['https://evil.example/https://ai.marin.cr/activate', false],
    [`https://${'name'}:${'word'}@ai.marin.cr/activate`, false], // credentials in the URL (built here so no scanner mistakes it for one)
    ['https://ai.marin.cr/activated', false],
    ['https://ai.marin.cr/activate.evil', false],
    ['https://ai.marin.cr/', false],
    ['https://ai.marin.cr/api/auth/me', false],
    ['file:///etc/passwd', false],
    ['javascript:alert(1)', false],
    ['not a url', false],
    ['', false],
    [undefined, false],
    [42, false],
  ])('isActivationUrl(%j) = %s', (url, expected) => {
    expect(isActivationUrl(url)).toBe(expected);
  });

  it.each([
    ['/api/auth/me', '/api/auth/me'],
    ['/api/localai/nodes?limit=5', '/api/localai/nodes?limit=5'],
    ['/api/pat/abc', '/api/pat/abc'],
    ['/auth/me', null],
    ['/api', null],
    ['//evil.example/api/x', null],
    ['/api/../x', null],
    ['/api/%2e%2e/x', null],
    ['/api/a%2f..%2fb', null],
    ['/api/%zz', null],
    [undefined, null],
  ])('cloudApiPath(%j) = %j', (path, expected) => {
    expect(cloudApiPath(path)).toBe(expected);
  });

  it.each([
    ['Person A', 'Person A'],
    ['  Person B  ', 'Person B'],
    ['person-a@example.test', undefined],
    ['', undefined],
    [null, undefined],
    ['A‮B\u0000C', 'ABC'],
    ['x'.repeat(200), `${'x'.repeat(80)}...`],
  ])('safeDisplayName(%j) = %j (never an email)', (raw, expected) => {
    expect(safeDisplayName(raw)).toBe(expected);
  });
});

describe('createCloudHttp', () => {
  const origin = 'https://ai.marin.cr';
  function fakeFetch(response: { status?: number; text?: string; length?: string }) {
    const seen: Array<{ url: string; init: Record<string, unknown> }> = [];
    const fn = (async (url: URL, init: Record<string, unknown>) => {
      seen.push({ url: url.toString(), init });
      return { status: response.status ?? 200, headers: { get: (name: string) => (name === 'content-length' ? (response.length ?? null) : null) }, text: async () => response.text ?? '' };
    }) as unknown as typeof fetch;
    return { fn, seen };
  }

  it('sends JSON, the bearer only when given, never follows a redirect, and parses the answer', async () => {
    const { fn, seen } = fakeFetch({ text: JSON.stringify({ data: { a: 1 } }) });
    const bearer = random('pat_');
    const result = await createCloudHttp({ fetch: fn, origin })('/api/x', { method: 'POST', body: { k: 'v' }, bearer });
    expect(result).toEqual({ status: 200, body: { data: { a: 1 } } });
    expect(seen[0]!.url).toBe('https://ai.marin.cr/api/x');
    expect(seen[0]!.init).toMatchObject({ method: 'POST', redirect: 'error', body: '{"k":"v"}' });
    expect(seen[0]!.init['headers']).toEqual({ accept: 'application/json', 'content-type': 'application/json', authorization: `Bearer ${bearer}` });
    await createCloudHttp({ fetch: fn, origin })('/api/y', { method: 'GET' });
    expect(seen[1]!.init['headers']).toEqual({ accept: 'application/json' });
    expect(seen[1]!.init).not.toHaveProperty('body');
  });

  it('a body that is not JSON, or empty, is null; an oversized one is an error', async () => {
    expect((await createCloudHttp({ fetch: fakeFetch({ text: '<html>' }).fn, origin })('/api/x', { method: 'GET' })).body).toBeNull();
    expect((await createCloudHttp({ fetch: fakeFetch({ status: 204 }).fn, origin })('/api/x', { method: 'GET' })).body).toBeNull();
    await expect(createCloudHttp({ fetch: fakeFetch({ length: String(10 * 1024 * 1024) }).fn, origin })('/api/x', { method: 'GET' })).rejects.toThrow(/too large/);
  });

  it('a path that leaves the origin is refused before fetch', async () => {
    const { fn, seen } = fakeFetch({});
    await expect(createCloudHttp({ fetch: fn, origin })('https://evil.example/x', { method: 'GET' })).rejects.toThrow(/another origin/);
    expect(seen).toHaveLength(0);
  });
});

describe('AbortRegistry', () => {
  it('abortAll aborts every controller once, synchronously, and forgets them', () => {
    const registry = new AbortRegistry();
    const a = registry.create();
    const b = registry.create();
    registry.release(b);
    const listener = vi.fn();
    a.signal.addEventListener('abort', listener);
    expect(registry.size).toBe(1);
    expect(registry.abortAll()).toBe(1);
    expect(listener).toHaveBeenCalledTimes(1);
    expect(b.signal.aborted).toBe(false);
    expect(registry.size).toBe(0);
    expect(registry.abortAll()).toBe(0);
  });
});

describe('LinkWindowController', () => {
  function fakeWindow() {
    const closed: Array<() => void> = [];
    const win = { destroyed: false, minimized: false, restore: vi.fn(), show: vi.fn(), focus: vi.fn(), close: vi.fn(() => { win.destroyed = true; closed.forEach((l) => l()); }) };
    const like: LinkWindowLike = { isDestroyed: () => win.destroyed, isMinimized: () => win.minimized, restore: win.restore, show: win.show, focus: win.focus, close: win.close, onClosed: (l) => void closed.push(l) };
    return { win, like };
  }

  it('one window at a time: a second open focuses it; closing it tells the owner, and the next open creates a new one', () => {
    const made: ReturnType<typeof fakeWindow>[] = [];
    const onClosed = vi.fn();
    const controller = new LinkWindowController({ factory: { create: () => (made.push(fakeWindow()), made.at(-1)!.like) }, title: () => 'Connect to the lab', onClosed });
    controller.open();
    expect(controller.isOpen).toBe(true);
    made[0]!.win.minimized = true;
    controller.open();
    expect(made).toHaveLength(1);
    expect(made[0]!.win.restore).toHaveBeenCalled();
    expect(made[0]!.win.focus).toHaveBeenCalled();
    controller.close();
    expect(onClosed).toHaveBeenCalledTimes(1);
    expect(controller.isOpen).toBe(false);
    controller.open();
    expect(made).toHaveLength(2);
  });
});
