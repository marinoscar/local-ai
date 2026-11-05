// Copy API key, the /key answer and the key actions (docs/specs/desktop.md 4.6): the clipboard clear rules, the masked
// form, the snippets that never hold the key, and the calls to the agent. Fakes for the clipboard, the timers and the agent.
import { randomBytes } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import { AgentTokenError } from '../../src/main/agent-client';
import {
  CLIPBOARD_CLEAR_MS,
  KeyCopier,
  LAB_BASE_URL,
  LOCAL_BASE_URL,
  buildConnectionInfo,
  copyNotification,
  createKeyActions,
  createKeyApi,
  maskKey,
  parseKeyResponse,
} from '../../src/main/clipboard';
import type { Timers } from '../../src/main/tray';

const newKey = (): string => randomBytes(32).toString('hex');
const json = (status: number, body: unknown, headers: Record<string, string> = {}): Response => new Response(JSON.stringify(body), { status, headers });

/** The agent's `GET /key` body for `key`. */
const keyBody = (key: string, over: Record<string, unknown> = {}): Record<string, unknown> => ({
  key,
  fingerprint: maskKey(key),
  base_urls: { local: 'http://127.0.0.1:8080/v1', tailnet: null, public: 'https://ai-n3.example.test/v1' },
  models: ['model-a', 'model-b'],
  snippets: {
    curl: 'curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $KEY"',
    env: 'OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=<key>',
    opencode: { provider: { 'local-llama': { options: { baseURL: 'http://127.0.0.1:8080/v1', apiKey: '{file:~/.config/opencode/llama-api-key}' } } } },
    openai_python: 'client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key=KEY)',
  },
  ...over,
});

/** A manual timer queue. */
function fakeTimers() {
  const pending: Array<{ ms: number; fn: () => void; cancelled: boolean }> = [];
  const timers: Timers = {
    after(ms, fn) {
      const entry = { ms, fn, cancelled: false };
      pending.push(entry);
      return () => void (entry.cancelled = true);
    },
  };
  return {
    timers,
    pending,
    /** Runs every timer that was not cancelled. */
    async fire() {
      for (const entry of pending.splice(0)) if (!entry.cancelled) entry.fn();
      await Promise.resolve();
      await Promise.resolve();
    },
  };
}

function fakeClipboard(initial = '') {
  const state = { text: initial, clears: 0, failRead: false, failWrite: false };
  return {
    state,
    port: {
      readText: () => {
        if (state.failRead) throw new Error('no clipboard');
        return state.text;
      },
      writeText: (text: string) => {
        if (state.failWrite) throw new Error('no clipboard');
        state.text = text;
      },
      clear: () => {
        state.text = '';
        state.clears += 1;
      },
    },
  };
}

describe('maskKey', () => {
  it('is the first four, an ellipsis and the last four characters', () => {
    expect(maskKey('ab12cdef0123456789ffee9z0f')).toBe('ab12…9z0f');
    const key = newKey();
    expect(maskKey(key)).toMatch(/^[0-9a-f]{4}…[0-9a-f]{4}$/);
    expect(maskKey(key)).toBe(`${key.slice(0, 4)}…${key.slice(60)}`);
    expect(maskKey(key)).not.toContain(key.slice(4, 60));
  });
  it('never returns a short value whole', () => {
    expect(maskKey('abc')).toBe('…');
  });
});

describe('parseKeyResponse', () => {
  it('accepts the shape of agent.md 3.8.13 and turns the OpenCode object into text', () => {
    const key = newKey();
    const parsed = parseKeyResponse(keyBody(key))!;
    expect(parsed.key).toBe(key);
    expect(parsed.baseUrls).toEqual({ local: 'http://127.0.0.1:8080/v1', tailnet: null, public: 'https://ai-n3.example.test/v1' });
    expect(parsed.models).toEqual(['model-a', 'model-b']);
    expect(JSON.parse(parsed.snippets.opencode!)).toMatchObject({ provider: { 'local-llama': { options: { apiKey: '{file:~/.config/opencode/llama-api-key}' } } } });
  });

  it.each([
    ['not an object', 'x'],
    ['no key', {}],
    ['a key of the wrong length', { key: 'ab12' }],
    ['upper-case hex', { key: 'A'.repeat(64) }],
    ['letters beyond f', { key: 'g'.repeat(64) }],
    ['a key that is not a string', { key: 12 }],
  ])('rejects %s', (_name, body) => {
    expect(parseKeyResponse(body)).toBeNull();
  });

  it('defaults the local URL and ignores snippets and models of the wrong type', () => {
    const parsed = parseKeyResponse({ key: newKey(), base_urls: { local: 5 }, models: ['ok', 4, null], snippets: { curl: 7 } })!;
    expect(parsed.baseUrls.local).toBe(LOCAL_BASE_URL);
    expect(parsed.models).toEqual(['ok']);
    expect(parsed.snippets).toEqual({});
  });
});

describe('buildConnectionInfo', () => {
  it('has the addresses and the masked key and not the key', () => {
    const key = newKey();
    const info = buildConnectionInfo(parseKeyResponse(keyBody(key))!);
    expect(info).toMatchObject({ ok: true, localUrl: LOCAL_BASE_URL, publicUrl: 'https://ai-n3.example.test/v1', tailnetUrl: null, labUrl: LAB_BASE_URL, keyMasked: maskKey(key), models: ['model-a', 'model-b'] });
    expect(LAB_BASE_URL).toBe('https://ai.marin.cr/v1');
    expect(JSON.stringify(info)).not.toContain(key);
    expect(info.snippets.curl).toContain('$KEY');
    expect(info.snippets.env).toContain('<key>');
  });

  it('removes the key from a snippet that happens to hold it', () => {
    const key = newKey();
    const info = buildConnectionInfo(parseKeyResponse(keyBody(key, { snippets: { curl: `curl -H "Authorization: Bearer ${key}"`, env: `OPENAI_API_KEY=${key}` } }))!);
    expect(JSON.stringify(info)).not.toContain(key);
    expect(info.snippets.curl).toContain('<key>');
  });

  it('builds placeholder snippets for an agent that sent none, from the local URL and the first model', () => {
    const key = newKey();
    const info = buildConnectionInfo(parseKeyResponse({ key, models: ['model-a'] })!);
    expect(info.snippets.opencode).toContain('"baseURL": "http://127.0.0.1:8080/v1"');
    expect(info.snippets.opencode).toContain('{file:~/.config/opencode/llama-api-key}');
    expect(info.snippets.opencode).toContain('model-a');
    expect(info.snippets.curl).toContain('$KEY');
    expect(JSON.stringify(info)).not.toContain(key);
  });
});

describe('createKeyApi', () => {
  it('show: GET /api/v1/key, the material back', async () => {
    const key = newKey();
    const request = vi.fn(async () => json(200, keyBody(key)));
    const shown = await createKeyApi({ request }).show();
    expect(request).toHaveBeenCalledWith('/api/v1/key');
    expect(shown).toMatchObject({ ok: true, material: { key } });
  });

  it('show: the agent’s error envelope comes back with its code and fix', async () => {
    const request = vi.fn(async () => json(500, { error: { code: 'LAI-SEC-010', message: 'The key file is damaged', fix: 'Run sudo aictl key rotate', details: { reason: 'malformed' } } }));
    expect(await createKeyApi({ request }).show()).toEqual({ ok: false, code: 'LAI-SEC-010', message: 'The key file is damaged', fix: 'Run sudo aictl key rotate' });
  });

  it.each([
    ['a 200 without a valid key', async () => json(200, { key: 'short' }), 'LAI-AGT-099'],
    ['a 401 with no envelope', async () => new Response('', { status: 401 }), 'LAI-DSK-007'],
    ['a 500 with no envelope', async () => new Response('oops', { status: 500 }), 'LAI-AGT-099'],
    ['an agent that is not running', async () => Promise.reject(new TypeError('fetch failed')), 'LAI-DSK-004'],
    ['a token file that cannot be read', async () => Promise.reject(new AgentTokenError('permission-denied')), 'LAI-DSK-001'],
    ['a token file that is missing', async () => Promise.reject(new AgentTokenError('missing')), 'LAI-DSK-003'],
  ])('show: %s is %s', async (_name, answer, code) => {
    expect(await createKeyApi({ request: vi.fn(answer) }).show()).toMatchObject({ ok: false, code });
  });

  it('rotate: one POST with the grant header, restart true and a long timeout', async () => {
    const request = vi.fn(async (..._args: unknown[]) =>
      json(200, { fingerprint: 'c0de…77aa', rotated_at: '2026-11-02T15:10:00Z', cloud_synced: true, restored_models: ['model-a'], clients_to_update: ['OpenCode on this computer: ~/.config/opencode/llama-api-key'], op_id: 'op_1' }),
    );
    const grant = `pg_${randomBytes(32).toString('base64url')}`;
    const result = await createKeyApi({ request }).rotate(grant);
    expect(request).toHaveBeenCalledTimes(1);
    expect(request).toHaveBeenCalledWith('/api/v1/key/rotate', { method: 'POST', json: { restart: true }, headers: { 'X-LocalAI-Pin-Grant': grant }, timeoutMs: 15 * 60 * 1000 });
    expect(result).toEqual({
      ok: true,
      fingerprint: 'c0de…77aa',
      rotatedAt: '2026-11-02T15:10:00Z',
      cloudSynced: true,
      restoredModels: ['model-a'],
      clientsToUpdate: ['OpenCode on this computer: ~/.config/opencode/llama-api-key'],
    });
  });

  it('rotate: a failure after the key was written says so, with the live fingerprint', async () => {
    const request = vi.fn(async () => json(500, { error: { code: 'LAI-LLM-001', message: 'llama did not restart', details: { key_rotated: true, fingerprint: 'c0de…77aa' } } }));
    expect(await createKeyApi({ request }).rotate('pg_x')).toEqual({ ok: false, code: 'LAI-LLM-001', message: 'llama did not restart', keyRotated: true, fingerprint: 'c0de…77aa' });
  });

  it('rotate: a refused grant is the agent’s code, key_rotated absent', async () => {
    const request = vi.fn(async () => json(403, { error: { code: 'LAI-SEC-002', message: 'needs the PIN', details: { pin_required: true } } }));
    const result = await createKeyApi({ request }).rotate('pg_x');
    expect(result).toEqual({ ok: false, code: 'LAI-SEC-002', message: 'needs the PIN' });
  });
});

describe('KeyCopier (the clear rules)', () => {
  function setup(over: { clearAfterMs?: number } = {}) {
    const key = newKey();
    const time = fakeTimers();
    const clipboard = fakeClipboard('something the user had copied before');
    const show = vi.fn(async () => ({ ok: true as const, material: parseKeyResponse(keyBody(key))! }));
    const copier = new KeyCopier({ keys: { show }, clipboard: clipboard.port, timers: time.timers, ...over });
    return { key, time, clipboard, show, copier };
  }

  it('copies the key, answers { ok: true, clearsInS: 60 } and schedules the clear after 60 s', async () => {
    const { key, time, clipboard, copier } = setup();
    expect(await copier.copy()).toEqual({ ok: true, clearsInS: 60 });
    expect(clipboard.state.text).toBe(key);
    expect(time.pending.map((p) => p.ms)).toEqual([CLIPBOARD_CLEAR_MS]);
    expect(CLIPBOARD_CLEAR_MS).toBe(60_000);
  });

  it('clears the clipboard when it still holds exactly that key', async () => {
    const { time, clipboard, copier } = setup();
    await copier.copy();
    await time.fire();
    expect(clipboard.state.text).toBe('');
    expect(clipboard.state.clears).toBe(1);
    expect(copier.pending).toBe(false);
  });

  it.each([
    ['something else', 'a different thing the user copied'],
    ['the key with a newline', 'KEY\n'],
    ['nothing', ''],
  ])('leaves the clipboard alone when it holds %s', async (_name, other) => {
    const { time, clipboard, copier, key } = setup();
    await copier.copy();
    clipboard.state.text = other === 'KEY\n' ? `${key}\n` : other;
    const before = clipboard.state.text;
    await time.fire();
    expect(clipboard.state.text).toBe(before);
    expect(clipboard.state.clears).toBe(0);
  });

  it('a second copy restarts the countdown: only one timer is live', async () => {
    const { time, copier, clipboard } = setup();
    await copier.copy();
    await copier.copy();
    expect(time.pending.filter((p) => !p.cancelled)).toHaveLength(1);
    await time.fire();
    expect(clipboard.state.clears).toBe(1);
  });

  it('a rotated key is a different key: the old copy is no longer cleared, the new one is', async () => {
    const { time, clipboard, copier, show } = setup();
    await copier.copy();
    const second = newKey();
    show.mockResolvedValueOnce({ ok: true as const, material: parseKeyResponse(keyBody(second))! });
    await copier.copy();
    expect(clipboard.state.text).toBe(second);
    await time.fire();
    expect(clipboard.state.text).toBe('');
  });

  it('the injected delay is honoured and reported (the e2e test copy)', async () => {
    const { time, copier } = setup({ clearAfterMs: 1500 });
    expect(await copier.copy()).toEqual({ ok: true, clearsInS: 2 });
    expect(time.pending[0]!.ms).toBe(1500);
    expect(setup({ clearAfterMs: 100 }).copier.clearsInS).toBe(1);
  });

  it('does not touch the clipboard when the agent refuses', async () => {
    const { clipboard, copier, show, time } = setup();
    show.mockResolvedValueOnce({ ok: false, code: 'LAI-DSK-004' } as never);
    expect(await copier.copy()).toEqual({ ok: false, code: 'LAI-DSK-004' });
    expect(clipboard.state.text).toBe('something the user had copied before');
    expect(time.pending).toEqual([]);
  });

  it('answers clipboard_failed when the clipboard cannot be written, and schedules nothing', async () => {
    const { clipboard, copier, time } = setup();
    clipboard.state.failWrite = true;
    expect(await copier.copy()).toEqual({ ok: false, code: 'clipboard_failed' });
    expect(time.pending).toEqual([]);
  });

  it('a clipboard that cannot be read at clear time is left alone', async () => {
    const { clipboard, copier, time } = setup();
    await copier.copy();
    clipboard.state.failRead = true;
    await time.fire();
    expect(clipboard.state.clears).toBe(0);
  });

  it('clearIfHeld (quit) clears the key now and cancels the timer; with nothing copied it does nothing', async () => {
    const { clipboard, copier, time } = setup();
    expect(await copier.clearIfHeld()).toBe(false);
    await copier.copy();
    expect(copier.pending).toBe(true);
    expect(await copier.clearIfHeld()).toBe(true);
    expect(clipboard.state.text).toBe('');
    expect(time.pending.every((p) => p.cancelled)).toBe(true);
    expect(await copier.clearIfHeld()).toBe(false);
  });

  it('never logs the key', async () => {
    const lines: string[] = [];
    const key = newKey();
    const time = fakeTimers();
    const clipboard = fakeClipboard();
    const log = { write: (entry: unknown) => void lines.push(JSON.stringify(entry)), log: (...args: unknown[]) => void lines.push(JSON.stringify(args)) };
    const copier = new KeyCopier({ keys: { show: async () => ({ ok: true, material: parseKeyResponse(keyBody(key))! }) }, clipboard: clipboard.port, timers: time.timers, log });
    await copier.copy();
    await time.fire();
    expect(lines.length).toBeGreaterThan(0);
    expect(lines.join('\n')).not.toContain(key);
  });
});

describe('createKeyActions', () => {
  function setup() {
    const key = newKey();
    const show = vi.fn(async () => ({ ok: true as const, material: parseKeyResponse(keyBody(key))! }));
    const rotate = vi.fn(async (_grant: string) => ({ ok: true as const, fingerprint: 'c0de…77aa', rotatedAt: '', cloudSynced: false, restoredModels: [], clientsToUpdate: [] }));
    const withGrant = vi.fn(async (action: (grant: string) => Promise<unknown>) => ({ ok: true as const, value: await action('pg_one') }));
    const actions = createKeyActions({ keys: { show, rotate }, copier: { copy: async () => ({ ok: true, clearsInS: 60 }) }, pin: { withGrant: withGrant as never } });
    return { key, show, rotate, withGrant, actions };
  }

  it('info shows the masked key; every reveal reads GET /key again (the agent audits each one)', async () => {
    const { key, show, actions } = setup();
    const info = await actions.info();
    expect(info).toMatchObject({ ok: true, keyMasked: maskKey(key) });
    expect(JSON.stringify(info)).not.toContain(key);
    expect(show).toHaveBeenCalledTimes(1);
    expect(await actions.reveal()).toEqual({ ok: true, key });
    expect(await actions.reveal()).toEqual({ ok: true, key });
    expect(show).toHaveBeenCalledTimes(3);
  });

  it('a refusal of GET /key reaches the page as it is', async () => {
    const { show, actions } = setup();
    show.mockResolvedValue({ ok: false, code: 'LAI-DSK-004' } as never);
    expect(await actions.info()).toEqual({ ok: false, code: 'LAI-DSK-004' });
    expect(await actions.reveal()).toEqual({ ok: false, code: 'LAI-DSK-004' });
  });

  it('rotate asks for the PIN for rotate-key and sends exactly one rotate request with that grant', async () => {
    const { rotate, withGrant, actions } = setup();
    expect(await actions.rotate()).toMatchObject({ ok: true, fingerprint: 'c0de…77aa' });
    expect(withGrant).toHaveBeenCalledWith(expect.any(Function), 'rotate-key');
    expect(rotate).toHaveBeenCalledTimes(1);
    expect(rotate).toHaveBeenCalledWith('pg_one');
  });

  it('a cancelled or refused PIN rotates nothing and says cancelled (the window already showed why)', async () => {
    const { rotate, withGrant, actions } = setup();
    withGrant.mockResolvedValueOnce({ ok: false, cancelled: true } as never);
    expect(await actions.rotate()).toEqual({ ok: false, code: 'LAI-SEC-002', cancelled: true });
    withGrant.mockResolvedValueOnce({ ok: false, cancelled: true, code: 'LAI-SEC-004' } as never);
    expect(await actions.rotate()).toEqual({ ok: false, code: 'LAI-SEC-004', cancelled: true });
    expect(rotate).not.toHaveBeenCalled();
  });
});

describe('copyNotification', () => {
  it.each([
    ['en', 'API key copied. It will be cleared from the clipboard in 60 seconds.'],
    ['es', 'Clave copiada. Se borrará del portapapeles en 60 segundos.'],
  ] as const)('says the spec text in %s', (lang, body) => {
    expect(copyNotification({ ok: true, clearsInS: 60 }, lang).body).toBe(body);
  });
  it('a failure carries the doctor code, a local failure does not', () => {
    expect(copyNotification({ ok: false, code: 'LAI-DSK-004' }, 'en').body).toBe('The API key could not be copied. (LAI-DSK-004)');
    expect(copyNotification({ ok: false, code: 'clipboard_failed' }, 'es').body).toBe('No se pudo copiar la clave de API.');
  });
});
