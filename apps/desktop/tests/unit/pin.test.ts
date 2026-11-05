// The PIN prompt (docs/specs/desktop.md 4.6): the service around the modal window, `POST /pin/verify`, the one-shot
// grant, and the texts the window shows for each refusal. Fake window, fake agent.
import { randomBytes } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import { GRANT_PATTERN, PIN_PATTERN, type PinApi, type PinPurpose, type PinWindowHandle, type VerifyOutcome, PinService, createPinApi } from '../../src/main/pin';
import { canSubmit, feedbackFor, parseQuery, sanitizePin } from '../../src/renderer/pin/model';

const newGrant = (): string => `pg_${randomBytes(32).toString('base64url')}`;
/** A PIN made at run time, so no literal PIN sits in the file. */
const newPin = (): string => String(10_000_000 + Math.floor(Math.random() * 89_999_999));

class FakeWindow implements PinWindowHandle {
  destroyed = false;
  focused = 0;
  closedListeners: Array<() => void> = [];
  constructor(readonly id: number) {}
  focus = () => void (this.focused += 1);
  isDestroyed = () => this.destroyed;
  onClosed = (listener: () => void) => void this.closedListeners.push(listener);
  close = (): void => {
    if (this.destroyed) return;
    this.destroyed = true;
    for (const l of this.closedListeners) l();
  };
}

function setup(answer: (pin: string) => VerifyOutcome | Promise<VerifyOutcome> = () => ({ ok: true, grant: newGrant() })) {
  const windows: FakeWindow[] = [];
  const opened: Array<{ purpose: PinPurpose; lang: string }> = [];
  const verify = vi.fn(async (pin: string) => answer(pin));
  const api: PinApi = { verify };
  const lines: string[] = [];
  const service = new PinService({
    api,
    lang: () => 'es',
    open: (purpose, lang) => {
      opened.push({ purpose, lang });
      const w = new FakeWindow(100 + windows.length);
      windows.push(w);
      return w;
    },
    log: { write: (entry) => void lines.push(JSON.stringify(entry)), log: (...args) => void lines.push(JSON.stringify(args)) },
  });
  return { service, windows, opened, verify, lines };
}

describe('PinService.requestGrant', () => {
  it('opens the window for the purpose in the UI language, and resolves with the grant once the PIN is accepted', async () => {
    const grant = newGrant();
    const { service, windows, opened, verify } = setup(() => ({ ok: true, grant }));
    const pending = service.requestGrant('leave-survival');
    expect(opened).toEqual([{ purpose: 'leave-survival', lang: 'es' }]);
    expect(service.windowId()).toBe(100);
    const pin = newPin();
    expect(await service.submit(100, pin)).toEqual({ ok: true });
    expect(await pending).toEqual({ ok: true, grant });
    expect(verify).toHaveBeenCalledWith(pin);
    // The window closed itself and nothing is left open.
    expect(windows[0]!.destroyed).toBe(true);
    expect(service.windowId()).toBeNull();
  });

  it('Cancel and closing the window both resolve as cancelled, and the grant is never asked for', async () => {
    const a = setup();
    const first = a.service.requestGrant('rotate-key');
    a.service.cancel(100);
    expect(await first).toEqual({ ok: false, cancelled: true });
    expect(a.windows[0]!.destroyed).toBe(true);

    const b = setup();
    const second = b.service.requestGrant('rotate-key');
    b.windows[0]!.close();
    expect(await second).toEqual({ ok: false, cancelled: true });
    expect(b.verify).not.toHaveBeenCalled();
  });

  it('only one prompt at a time: a second request focuses the first and answers cancelled', async () => {
    const { service, windows, opened } = setup();
    const first = service.requestGrant('leave-survival');
    expect(await service.requestGrant('rotate-key')).toEqual({ ok: false, cancelled: true });
    expect(opened).toHaveLength(1);
    expect(windows[0]!.focused).toBe(1);
    service.cancel(100);
    await first;
    // Once finished a new prompt opens.
    void service.requestGrant('rotate-key');
    expect(opened).toHaveLength(2);
  });

  it('a refusal stays in the window; closing the window then reports its code as cancelled (the user saw it)', async () => {
    const { service } = setup(() => ({ ok: false, code: 'LAI-SEC-004', retryAfterS: 300 }));
    const pending = service.requestGrant('leave-survival');
    expect(await service.submit(100, newPin())).toEqual({ ok: false, code: 'LAI-SEC-004', retryAfterS: 300 });
    expect(service.windowId()).toBe(100);
    service.cancel(100);
    expect(await pending).toEqual({ ok: false, cancelled: true, code: 'LAI-SEC-004' });
  });

  it('a wrong PIN can be retried in the same window and then succeed', async () => {
    const grant = newGrant();
    const answers: VerifyOutcome[] = [{ ok: false, code: 'LAI-SEC-003', remainingAttempts: 4 }, { ok: true, grant }];
    const { service } = setup(() => answers.shift()!);
    const pending = service.requestGrant('leave-survival');
    expect(await service.submit(100, newPin())).toEqual({ ok: false, code: 'LAI-SEC-003', remainingAttempts: 4 });
    expect(await service.submit(100, newPin())).toEqual({ ok: true });
    expect(await pending).toEqual({ ok: true, grant });
  });
});

describe('PinService.submit', () => {
  it('refuses every sender but the PIN window, and a call with no prompt open', async () => {
    const { service, verify } = setup();
    expect(await service.submit(100, newPin())).toEqual({ ok: false, code: 'no_prompt' });
    void service.requestGrant('leave-survival');
    expect(await service.submit(7, newPin())).toEqual({ ok: false, code: 'no_prompt' });
    service.cancel(7); // not the window: ignored
    expect(service.windowId()).toBe(100);
    expect(verify).not.toHaveBeenCalled();
  });

  it.each([['123'], ['123456789'], ['12a4'], [''], ['１２３４'], [1234], [null], [{ pin: '1234' }]])('does not send %j to the agent', async (value) => {
    const { service, verify } = setup();
    void service.requestGrant('leave-survival');
    expect(await service.submit(100, value)).toEqual({ ok: false, code: 'invalid_format' });
    expect(verify).not.toHaveBeenCalled();
  });

  it('answers busy to a second submit while the agent is still checking', async () => {
    let release: (outcome: VerifyOutcome) => void = () => undefined;
    const { service, verify } = setup(() => new Promise<VerifyOutcome>((resolve) => (release = resolve)));
    const pending = service.requestGrant('leave-survival');
    const first = service.submit(100, newPin());
    expect(await service.submit(100, newPin())).toEqual({ ok: false, code: 'busy' });
    release({ ok: true, grant: newGrant() });
    expect(await first).toEqual({ ok: true });
    await pending;
    expect(verify).toHaveBeenCalledTimes(1);
  });

  it('drops a grant that arrives after the window was closed', async () => {
    let release: (outcome: VerifyOutcome) => void = () => undefined;
    const { service, windows } = setup(() => new Promise<VerifyOutcome>((resolve) => (release = resolve)));
    const pending = service.requestGrant('leave-survival');
    const submitted = service.submit(100, newPin());
    windows[0]!.close();
    release({ ok: true, grant: newGrant() });
    expect(await submitted).toEqual({ ok: false, code: 'no_prompt' });
    expect(await pending).toEqual({ ok: false, cancelled: true });
  });

  it('never puts the PIN or the grant in a log line, and never in the answer', async () => {
    const grant = newGrant();
    const pin = newPin();
    const { service, lines } = setup(() => ({ ok: true, grant }));
    const pending = service.requestGrant('leave-survival');
    const answer = await service.submit(100, pin);
    await pending;
    expect(lines.length).toBeGreaterThan(0);
    expect(lines.join('\n')).not.toContain(pin);
    expect(lines.join('\n')).not.toContain(grant);
    expect(JSON.stringify(answer)).not.toContain(pin);
    expect(JSON.stringify(answer)).not.toContain(grant);
  });

  it('logs a refusal by code only', async () => {
    const { service, lines } = setup(() => ({ ok: false, code: 'LAI-SEC-003', remainingAttempts: 2 }));
    void service.requestGrant('leave-survival');
    const pin = newPin();
    await service.submit(100, pin);
    expect(lines.join('\n')).toContain('LAI-SEC-003');
    expect(lines.join('\n')).not.toContain(pin);
  });
});

describe('PinService.withGrant', () => {
  it('runs the action once with the grant and returns its value', async () => {
    const grant = newGrant();
    const { service } = setup(() => ({ ok: true, grant }));
    const action = vi.fn(async (_g: string) => 'done');
    const pending = service.withGrant(action, 'rotate-key');
    await Promise.resolve();
    await service.submit(100, newPin());
    expect(await pending).toEqual({ ok: true, value: 'done' });
    expect(action).toHaveBeenCalledTimes(1);
    expect(action).toHaveBeenCalledWith(grant);
  });

  it('defaults to the leave-survival prompt', async () => {
    const { service, opened } = setup();
    void service.withGrant(async () => 1);
    expect(opened[0]!.purpose).toBe('leave-survival');
  });

  it('does not run the action when the user cancels', async () => {
    const { service } = setup();
    const action = vi.fn(async () => 1);
    const pending = service.withGrant(action);
    service.cancel(100);
    expect(await pending).toEqual({ ok: false, cancelled: true });
    expect(action).not.toHaveBeenCalled();
  });

  it('a grant is not kept: the next call needs a new PIN', async () => {
    const { service, opened, verify } = setup();
    const first = service.withGrant(async () => 1);
    await service.submit(100, newPin());
    await first;
    const second = service.withGrant(async () => 2);
    expect(opened).toHaveLength(2);
    await service.submit(101, newPin());
    await second;
    expect(verify).toHaveBeenCalledTimes(2);
  });
});

describe('createPinApi', () => {
  const json = (status: number, body: unknown, headers: Record<string, string> = {}): Response => new Response(JSON.stringify(body), { status, headers });
  const verify = (answer: Response | Error) => {
    const request = vi.fn(async (..._args: unknown[]) => {
      if (answer instanceof Error) throw answer;
      return answer;
    });
    return { request, result: (pin: string) => createPinApi({ request }).verify(pin) };
  };

  it('POSTs { pin } to /api/v1/pin/verify and returns the grant', async () => {
    const grant = newGrant();
    const { request, result } = verify(json(200, { grant, expires_at: '2026-11-02T15:09:05Z' }));
    const pin = newPin();
    expect(await result(pin)).toEqual({ ok: true, grant });
    expect(request).toHaveBeenCalledWith('/api/v1/pin/verify', { method: 'POST', json: { pin } });
    expect(GRANT_PATTERN.test(grant)).toBe(true);
  });

  it('a 200 whose grant is not pg_ and 43 characters is not accepted', async () => {
    expect(await verify(json(200, { grant: 'pg_short' })).result('1234')).toEqual({ ok: false, code: 'LAI-AGT-099' });
    expect(await verify(json(200, {})).result('1234')).toEqual({ ok: false, code: 'LAI-AGT-099' });
  });

  it('401 LAI-SEC-003 carries the remaining tries', async () => {
    const { result } = verify(json(401, { error: { code: 'LAI-SEC-003', message: 'Wrong PIN', fix: 'Try again.', details: { remaining_attempts: 3 } } }));
    expect(await result('1234')).toEqual({ ok: false, code: 'LAI-SEC-003', fix: 'Try again.', remainingAttempts: 3 });
  });

  it('429 LAI-SEC-004 carries the wait, from the details or from Retry-After', async () => {
    const fromDetails = verify(json(429, { error: { code: 'LAI-SEC-004', details: { retry_after_s: 290, locked: false } } }, { 'Retry-After': '10' }));
    expect(await fromDetails.result('1234')).toEqual({ ok: false, code: 'LAI-SEC-004', retryAfterS: 290 });
    const fromHeader = verify(json(429, { error: { code: 'LAI-SEC-004' } }, { 'Retry-After': '120' }));
    expect(await fromHeader.result('1234')).toEqual({ ok: false, code: 'LAI-SEC-004', retryAfterS: 120 });
  });

  it('409 LAI-SEC-005 (no PIN set) and an unreachable agent', async () => {
    expect(await verify(json(409, { error: { code: 'LAI-SEC-005', fix: 'Run sudo aictl pin set' } })).result('1234')).toEqual({ ok: false, code: 'LAI-SEC-005', fix: 'Run sudo aictl pin set' });
    expect(await verify(new TypeError('fetch failed')).result('1234')).toEqual({ ok: false, code: 'LAI-DSK-004' });
  });
});

describe('the PIN window’s texts', () => {
  const text = (result: Parameters<typeof feedbackFor>[0], lang: 'en' | 'es' = 'en') => feedbackFor(result, lang);

  it('a success has no feedback', () => {
    expect(text({ ok: true })).toBeNull();
  });

  it('a wrong PIN says how many tries are left, in both languages, with its code', () => {
    expect(text({ ok: false, code: 'LAI-SEC-003', remainingAttempts: 4 })).toMatchObject({ text: 'Wrong PIN. 4 tries left.', code: 'LAI-SEC-003', final: false });
    expect(text({ ok: false, code: 'LAI-SEC-003', remainingAttempts: 1 })?.text).toBe('Wrong PIN. 1 try left.');
    expect(text({ ok: false, code: 'LAI-SEC-003', remainingAttempts: 4 }, 'es')?.text).toBe('PIN incorrecto. Quedan 4 intentos.');
    expect(text({ ok: false, code: 'LAI-SEC-003' })?.text).toBe('Wrong PIN.');
  });

  it('too many tries says to wait the 5 minutes and is final', () => {
    expect(text({ ok: false, code: 'LAI-SEC-004', retryAfterS: 300 })).toMatchObject({ text: 'Too many tries. Wait 5 minutes.', code: 'LAI-SEC-004', final: true });
    expect(text({ ok: false, code: 'LAI-SEC-004' })?.text).toBe('Too many tries. Wait 5 minutes.');
    expect(text({ ok: false, code: 'LAI-SEC-004', retryAfterS: 45 })?.text).toBe('Too many tries. Wait 1 minute.');
    expect(text({ ok: false, code: 'LAI-SEC-004', retryAfterS: 3600 }, 'es')?.text).toBe('Demasiados intentos. Espera 60 minutos.');
  });

  it('no PIN set shows the command to set one and is final', () => {
    const feedback = text({ ok: false, code: 'LAI-SEC-005' })!;
    expect(feedback).toMatchObject({ code: 'LAI-SEC-005', final: true });
    expect(feedback.fix).toContain('sudo aictl pin set');
  });

  it('anything else is a generic message; only a doctor code is shown as a code', () => {
    expect(text({ ok: false, code: 'LAI-DSK-004' })).toMatchObject({ text: 'The PIN could not be checked.', code: 'LAI-DSK-004', final: false });
    expect(text({ ok: false, code: 'busy' })?.code).toBeUndefined();
  });

  it('the field keeps digits only, at most 8, and submits from 4', () => {
    expect(sanitizePin('12a3-4 5')).toBe('12345');
    expect(sanitizePin('1234567890')).toBe('12345678');
    expect(['', '123', '1234', '12345678'].map(canSubmit)).toEqual([false, false, true, true]);
    expect(PIN_PATTERN.test('1234')).toBe(true);
  });

  it('the query picks the purpose and language and falls back to the safe reading', () => {
    expect(parseQuery('?purpose=rotate-key&lang=es')).toEqual({ purpose: 'rotate-key', lang: 'es' });
    expect(parseQuery('?purpose=<script>&lang=fr')).toEqual({ purpose: 'leave-survival', lang: 'en' });
    expect(parseQuery('')).toEqual({ purpose: 'leave-survival', lang: 'en' });
  });
});
