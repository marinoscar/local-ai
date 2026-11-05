// The device flow (docs/specs/desktop.md 4.8, EAB DEVICE-AUTH.md): the grant request and the polling state machine,
// against a scripted fake of the cloud. No network, no timers: the clock is advanced by the fake `sleep`.
import { randomBytes, randomUUID } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { type CloudHttp, type CloudHttpInit, type CloudHttpResult, MAX_POLL_FAILURES, SLOW_DOWN_MS, pollDeviceToken, requestDeviceGrant } from '../../src/main/cloud-link';

const random = (prefix: string): string => `${prefix}${randomBytes(24).toString('base64url')}`;
const envelope = (data: unknown): unknown => ({ data, meta: { timestamp: '2026-11-05T12:00:00.000Z' } });
const rfc = (error: string): CloudHttpResult => ({ status: 400, body: { error, error_description: 'x' } });
const failure = Symbol('network failure');
const PAT_ID = randomUUID();

function pat(overrides: Record<string, unknown> = {}): CloudHttpResult {
  return {
    status: 200,
    body: envelope({
      accessToken: random('pat_'),
      tokenType: 'Bearer',
      expiresIn: 7775999,
      credentialType: 'pat',
      expiresAt: '2027-02-03T12:00:00.000Z',
      tokenId: PAT_ID,
      tokenName: 'Device: ai-desktop on host-a',
      ...overrides,
    }),
  };
}

interface Script {
  http: CloudHttp;
  bodies: unknown[];
  sleeps: number[];
  now(): number;
  sleep(ms: number, signal: AbortSignal): Promise<void>;
}

/** Answers the polls in order; `failure` throws as a network error does. The fake clock moves by each sleep. */
function script(answers: Array<CloudHttpResult | typeof failure>): Script {
  let clock = 1_000_000;
  const sleeps: number[] = [];
  const bodies: unknown[] = [];
  let next = 0;
  return {
    bodies,
    sleeps,
    now: () => clock,
    sleep: async (ms) => {
      sleeps.push(ms);
      clock += ms;
    },
    http: async (_path: string, init: CloudHttpInit) => {
      bodies.push(init.body);
      const answer = answers[next++];
      if (answer === undefined) throw new Error('the script ran out of answers');
      if (answer === failure) throw new TypeError('fetch failed');
      return answer;
    },
  };
}

const poll = (s: Script, extra: { intervalS?: number; expiresInS?: number; signal?: AbortSignal } = {}) =>
  pollDeviceToken({ http: s.http, deviceCode: 'device-code-for-test', intervalS: extra.intervalS ?? 5, expiresInS: extra.expiresInS ?? 900, sleep: s.sleep, now: s.now, signal: extra.signal ?? new AbortController().signal });

describe('pollDeviceToken', () => {
  it('pending, pending, then the PAT: one poll per interval, the device code in the body', async () => {
    const s = script([rfc('authorization_pending'), rfc('authorization_pending'), pat()]);
    const outcome = await poll(s);
    expect(outcome.kind).toBe('approved');
    expect(s.sleeps).toEqual([5000, 5000, 5000]);
    expect(s.bodies).toEqual([{ deviceCode: 'device-code-for-test' }, { deviceCode: 'device-code-for-test' }, { deviceCode: 'device-code-for-test' }]);
    if (outcome.kind === 'approved') {
      expect(outcome.credential.token.startsWith('pat_')).toBe(true);
      expect(outcome.credential.tokenId).toBe(PAT_ID);
      expect(outcome.credential.expiresAt).toBe('2027-02-03T12:00:00.000Z');
    }
  });

  it('slow_down adds 5 s to every later wait, and a second one adds 5 s more', async () => {
    const s = script([rfc('authorization_pending'), rfc('slow_down'), rfc('authorization_pending'), rfc('slow_down'), pat()]);
    const slowed: number[] = [];
    const outcome = await pollDeviceToken({ http: s.http, deviceCode: 'x', intervalS: 5, expiresInS: 900, sleep: s.sleep, now: s.now, signal: new AbortController().signal, onSlowDown: (ms) => slowed.push(ms) });
    expect(outcome.kind).toBe('approved');
    expect(s.sleeps).toEqual([5000, 5000, 5000 + SLOW_DOWN_MS, 5000 + SLOW_DOWN_MS, 5000 + 2 * SLOW_DOWN_MS]);
    expect(slowed).toEqual([10_000, 15_000]);
  });

  it('an HTTP 429 slows down like slow_down', async () => {
    const s = script([{ status: 429, body: null }, pat()]);
    expect((await poll(s)).kind).toBe('approved');
    expect(s.sleeps).toEqual([5000, 10_000]);
  });

  it.each([
    ['expired_token', 'expired'],
    ['access_denied', 'denied'],
    ['invalid_grant', 'failed'],
    ['', 'failed'],
  ])('the RFC 8628 error %j ends the attempt as %s', async (error, kind) => {
    const outcome = await poll(script([rfc('authorization_pending'), error === '' ? { status: 400, body: null } : rfc(error)]));
    expect(outcome.kind).toBe(kind);
  });

  it('stops after expiresIn without polling again (15 s at 5 s: two polls)', async () => {
    const s = script([rfc('authorization_pending'), rfc('authorization_pending'), rfc('authorization_pending')]);
    const outcome = await poll(s, { expiresInS: 15 });
    expect(outcome.kind).toBe('expired');
    expect(s.bodies).toHaveLength(2);
  });

  describe('credentialType', () => {
    it.each([
      ['a session (no credentialType): never stored', { credentialType: undefined, accessToken: ['header', 'payload', 'signature'].join('.'), refreshToken: 'r' }],
      ['credentialType "session"', { credentialType: 'session' }],
      ['an access token that is not a pat_', { accessToken: 'not-a-pat-value' }],
      ['a tokenId that is not a plain id', { tokenId: '../../etc' }],
      ['no expiresAt', { expiresAt: undefined }],
      ['an expiresAt that is no date', { expiresAt: 'tomorrow' }],
    ])('%s -> failed', async (_name, overrides) => {
      expect((await poll(script([pat(overrides as Record<string, unknown>)]))).kind).toBe('failed');
    });

    it('a 200 without the data envelope -> failed', async () => {
      expect((await poll(script([{ status: 200, body: { accessToken: random('pat_') } }]))).kind).toBe('failed');
    });
  });

  describe('network failures', () => {
    it(`${MAX_POLL_FAILURES} in a row -> unreachable`, async () => {
      const s = script(Array.from({ length: MAX_POLL_FAILURES }, () => failure));
      expect((await poll(s)).kind).toBe('unreachable');
      expect(s.bodies).toHaveLength(MAX_POLL_FAILURES);
    });

    it('a few failures then success -> approved (the count resets on an answer)', async () => {
      const s = script([failure, failure, rfc('authorization_pending'), failure, failure, failure, pat()]);
      expect((await poll(s)).kind).toBe('approved');
    });

    it('5xx answers count like a network failure', async () => {
      const s = script(Array.from({ length: MAX_POLL_FAILURES }, () => ({ status: 503, body: null }) as CloudHttpResult));
      expect((await poll(s)).kind).toBe('unreachable');
    });
  });

  describe('cancel', () => {
    it('a signal aborted before the first wait -> cancelled, no request', async () => {
      const controller = new AbortController();
      controller.abort();
      const s = script([pat()]);
      expect((await poll(s, { signal: controller.signal })).kind).toBe('cancelled');
      expect(s.bodies).toHaveLength(0);
    });

    it('aborted during the wait -> cancelled, no request', async () => {
      const controller = new AbortController();
      const s = script([pat()]);
      const outcome = await pollDeviceToken({
        http: s.http, deviceCode: 'x', intervalS: 5, expiresInS: 900, now: s.now, signal: controller.signal,
        sleep: async () => controller.abort(),
      });
      expect(outcome.kind).toBe('cancelled');
      expect(s.bodies).toHaveLength(0);
    });

    it('aborted during the request (the call throws) -> cancelled, not a network failure', async () => {
      const controller = new AbortController();
      const outcome = await pollDeviceToken({
        http: async () => {
          controller.abort();
          throw new DOMException('aborted', 'AbortError');
        },
        deviceCode: 'x', intervalS: 5, expiresInS: 900, now: () => 0, signal: controller.signal, sleep: async () => undefined,
      });
      expect(outcome.kind).toBe('cancelled');
    });
  });
});

describe('requestDeviceGrant', () => {
  const grantBody = (over: Record<string, unknown> = {}): CloudHttpResult => ({
    status: 201,
    body: envelope({ deviceCode: 'dc', userCode: 'ABCD-1234', verificationUri: 'https://ai.marin.cr/activate', verificationUriComplete: 'https://ai.marin.cr/activate?code=ABCD-1234', expiresIn: 900, interval: 5, ...over }),
  });
  const info = { deviceName: 'ai-desktop on host-a', userAgent: 'ai-desktop/0.1.0' };

  it('asks for a pat with the device name and user agent, and no token or code', async () => {
    const seen: Array<{ path: string; init: CloudHttpInit }> = [];
    const result = await requestDeviceGrant(async (path, init) => (seen.push({ path, init }), grantBody()), info);
    expect(seen).toHaveLength(1);
    expect(seen[0]!.path).toBe('/api/auth/device/code');
    expect(seen[0]!.init.method).toBe('POST');
    expect(seen[0]!.init.bearer).toBeUndefined();
    expect(seen[0]!.init.body).toEqual({ clientInfo: { deviceName: 'ai-desktop on host-a', userAgent: 'ai-desktop/0.1.0', tokenType: 'pat' } });
    expect(result).toMatchObject({ ok: true, grant: { userCode: 'ABCD-1234', expiresIn: 900, interval: 5, deviceCode: 'dc' } });
  });

  it.each([
    ['interval 0 -> 1', { interval: 0 }, 1],
    ['interval 600 -> 60', { interval: 600 }, 60],
    ['no interval -> 5', { interval: undefined }, 5],
  ])('%s', async (_name, over, expected) => {
    const result = await requestDeviceGrant(async () => grantBody(over), info);
    expect(result.ok && result.grant.interval).toBe(expected);
  });

  it.each([
    ['no deviceCode', { deviceCode: '' }],
    ['no userCode', { userCode: undefined }],
    ['expiresIn 0', { expiresIn: 0 }],
    ['expiresIn a string', { expiresIn: '900' }],
  ])('%s -> unavailable', async (_name, over) => {
    expect(await requestDeviceGrant(async () => grantBody(over), info)).toEqual({ ok: false, kind: 'unavailable' });
  });

  it('the device flow turned off (404) -> unavailable; 503 and a thrown error -> unreachable', async () => {
    expect(await requestDeviceGrant(async () => ({ status: 404, body: null }), info)).toEqual({ ok: false, kind: 'unavailable' });
    expect(await requestDeviceGrant(async () => ({ status: 503, body: null }), info)).toEqual({ ok: false, kind: 'unreachable' });
    expect(await requestDeviceGrant(async () => Promise.reject(new TypeError('fetch failed')), info)).toEqual({ ok: false, kind: 'unreachable' });
  });
});
