// "Install what's missing" in the main process (issue #480, ADR-0010, docs/specs/desktop.md 4.12): the picture parsed from
// GET /api/v1/ensure, the three calls with their bearer and language, and the confirmation path that needs the PIN.
import { describe, expect, it, vi } from 'vitest';
import { type AgentClient } from '../../src/main/agent-client';
import { type GrantRunner } from '../../src/main/clipboard';
import { createEnsureActions, createEnsureApi, parseConfirmSteps, parseEnsureView } from '../../src/main/ensure';
import { PIN_GRANT_HEADER } from '../../src/main/tray';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';

const GRANT = `pg_${'A'.repeat(43)}`;

const view = (state: string, steps: Array<Record<string, unknown>> = []): Record<string, unknown> => ({
  state,
  auto: 'on',
  message: `message ${state}`,
  next_action: { kind: 'press_button', text: 'Press the button.', step: 'tool.piper' },
  progress: { done: 1, total: 3, current: 'tool.piper' },
  steps,
  run: null,
  dry_run: false,
  checked_at: '2026-11-10T10:00:00Z',
});

describe('parseEnsureView', () => {
  it('keeps what the window shows and drops the rest', () => {
    const parsed = parseEnsureView({
      ...view('needs_you', [
        { id: 'tool.piper', kind: 'install_tool', status: 'pending', needs_confirmation: false, title: 'Voice', detail: 'From the kit.', error: 'a secret path /etc/x', item: 'x' },
        { id: 'whisper.build', status: 'needs_confirm', needs_confirmation: true, title: 'Build', detail: '', code: 'LAI-KIT-052' },
      ]),
      extra: 'dropped',
    });
    expect(parsed).toEqual({
      state: 'needs_you',
      auto: 'on',
      message: 'message needs_you',
      nextAction: { kind: 'press_button', text: 'Press the button.', step: 'tool.piper' },
      progress: { done: 1, total: 3, current: 'tool.piper' },
      steps: [
        { id: 'tool.piper', status: 'pending', title: 'Voice', detail: 'From the kit.', needsConfirmation: false },
        { id: 'whisper.build', status: 'needs_confirm', title: 'Build', detail: '', needsConfirmation: true, code: 'LAI-KIT-052' },
      ],
    });
  });

  it('refuses a body that is not the picture, skips a step it does not understand and cuts long texts', () => {
    expect(parseEnsureView(null)).toBeNull();
    expect(parseEnsureView({ state: 'exploded' })).toBeNull();
    expect(parseEnsureView([])).toBeNull();
    const parsed = parseEnsureView({ ...view('complete', [{ id: 'x', status: 'strange' }, { id: 'y' }, { id: 'tool.pmtiles', status: 'ok', title: 'x'.repeat(5000) }]) });
    expect(parsed!.steps).toHaveLength(1);
    expect(parsed!.steps[0]!.title.length).toBeLessThanOrEqual(600);
  });

  it('falls back to safe values for a missing auto, next action or progress', () => {
    const parsed = parseEnsureView({ state: 'complete', steps: [] });
    expect(parsed).toMatchObject({ auto: 'on', nextAction: { kind: 'none', text: '' }, progress: { done: 0, total: 0 } });
  });
});

describe('parseConfirmSteps', () => {
  it('accepts only the two steps the agent holds back, each once', () => {
    expect(parseConfirmSteps(['whisper.build'])).toEqual(['whisper.build']);
    expect(parseConfirmSteps(['restart.kiwix', 'whisper.build'])).toEqual(['restart.kiwix', 'whisper.build']);
  });
  it.each([[[]], [['tool.piper']], [['whisper.build', 'whisper.build']], [['whisper.build', 'restart.kiwix', 'x']], ['whisper.build'], [null], [[7]]])('refuses %j', (raw) => {
    expect(parseConfirmSteps(raw)).toBeNull();
  });
});

describe('the calls over a fake agent', () => {
  async function withAgent<T>(run: (agent: FakeAgent, api: ReturnType<typeof createEnsureApi>) => Promise<T>): Promise<T> {
    const agent = await startFakeAgent({ port: 0 });
    const client = {
      request: (path: string, options: { method?: string; json?: unknown; headers?: Record<string, string> } = {}) =>
        fetch(`${agent.url}${path}`, {
          method: options.method ?? 'GET',
          headers: { authorization: `Bearer ${agent.token}`, ...(options.json !== undefined ? { 'content-type': 'application/json' } : {}), ...(options.headers ?? {}) },
          ...(options.json !== undefined ? { body: JSON.stringify(options.json) } : {}),
        }),
    } as unknown as Pick<AgentClient, 'request'>;
    try {
      return await run(agent, createEnsureApi(client));
    } finally {
      await agent.close();
    }
  }

  it('view asks in the UI language and returns the picture', async () => {
    await withAgent(async (agent, api) => {
      const result = await api.view('es');
      expect(result).toMatchObject({ ok: true, running: false, view: { state: 'needs_you', steps: [{ id: 'tool.piper', status: 'pending' }] } });
      expect(agent.ensureRequests[0]).toMatchObject({ method: 'GET', language: 'es' });
    });
  });

  it('start sends the safe default (an empty object) and no PIN grant; 202 means running', async () => {
    await withAgent(async (agent, api) => {
      const result = await api.start('en');
      expect(result).toMatchObject({ ok: true, running: true, view: { state: 'installing' } });
      expect(agent.ensureRequests[0]).toMatchObject({ method: 'POST', body: {}, pinGrant: undefined, language: 'en' });
    });
  });

  it('cancel is a DELETE and shows what is left', async () => {
    await withAgent(async (agent, api) => {
      const result = await api.cancel('en');
      expect(result).toMatchObject({ ok: true, running: true, view: { state: 'needs_you', steps: [{ status: 'cancelled' }] } });
      expect(agent.ensureRequests[0]!.method).toBe('DELETE');
    });
  });

  it.each([
    [409, 'LAI-SYN-005'],
    [409, 'LAI-AGT-022'],
    [503, 'LAI-AGT-024'],
  ])('a %i %s refusal keeps its code, message and fix', async (status, code) => {
    await withAgent(async (agent, api) => {
      agent.failNextEnsure = { status, code, message: 'A copy is running', fix: 'Try again when it ends.' };
      expect(await api.start('en')).toEqual({ ok: false, status, code, message: 'A copy is running', fix: 'Try again when it ends.' });
      // Then back to accepting.
      expect((await api.start('en')).ok).toBe(true);
    });
  });

  it('a confirmation without the PIN grant is refused by the agent with LAI-SEC-002', async () => {
    await withAgent(async (_agent, api) => {
      const result = await api.start('en', { confirm: ['whisper.build'], grant: 'pg_wrong' });
      expect(result).toMatchObject({ ok: false, status: 403, code: 'LAI-SEC-002' });
    });
  });

  it('an agent that cannot be reached is LAI-DSK-004; a body that is not the picture is LAI-AGT-099', async () => {
    const down = createEnsureApi({ request: async () => Promise.reject(new Error('ECONNREFUSED')) } as unknown as Pick<AgentClient, 'request'>);
    expect(await down.view('en')).toEqual({ ok: false, code: 'LAI-DSK-004' });
    const odd = createEnsureApi({ request: async () => new Response(JSON.stringify({ hello: 1 }), { status: 200 }) } as unknown as Pick<AgentClient, 'request'>);
    expect(await odd.view('en')).toEqual({ ok: false, code: 'LAI-AGT-099' });
  });
});

describe('createEnsureActions', () => {
  const picture = parseEnsureView(view('installing'))!;
  function setup(grantOutcome: 'ok' | 'cancelled' = 'ok') {
    const start = vi.fn(async (..._args: unknown[]) => ({ ok: true as const, view: picture, running: true }));
    const api = { view: vi.fn(async () => ({ ok: true as const, view: picture, running: false })), start, cancel: vi.fn(async () => ({ ok: true as const, view: picture, running: false })) };
    const withGrant = vi.fn(async (action: (grant: string) => Promise<unknown>, purpose?: string) => {
      void purpose;
      return grantOutcome === 'ok' ? { ok: true as const, value: await action(GRANT) } : { ok: false as const, cancelled: true, code: 'LAI-SEC-003' };
    });
    const actions = createEnsureActions({ api, pin: { withGrant } as unknown as GrantRunner, lang: () => 'es' });
    return { actions, api, withGrant, start };
  }

  it('view, start and cancel use the UI language and ask for no PIN', async () => {
    const { actions, api, withGrant } = setup();
    await actions.view();
    await actions.start();
    await actions.cancel();
    expect(api.view).toHaveBeenCalledWith('es');
    expect(api.start).toHaveBeenCalledWith('es');
    expect(api.cancel).toHaveBeenCalledWith('es');
    expect(withGrant).not.toHaveBeenCalled();
  });

  it('confirm asks for the PIN for the confirm-ensure purpose, then sends the steps with the grant of that one request', async () => {
    const { actions, withGrant, start } = setup();
    expect(await actions.confirm(['whisper.build'])).toMatchObject({ ok: true, running: true });
    expect(withGrant).toHaveBeenCalledTimes(1);
    expect(withGrant.mock.calls[0]![1]).toBe('confirm-ensure');
    expect(start).toHaveBeenCalledWith('es', { confirm: ['whisper.build'], grant: GRANT });
  });

  it('a closed PIN window asks nothing of the agent', async () => {
    const { actions, start } = setup('cancelled');
    expect(await actions.confirm(['restart.kiwix'])).toEqual({ ok: false, code: 'LAI-SEC-003', cancelled: true });
    expect(start).not.toHaveBeenCalled();
  });

  it('a step that is not one of the two is refused before the PIN window opens', async () => {
    const { actions, withGrant, start } = setup();
    for (const raw of [['tool.piper'], [], 'whisper.build', undefined, ['whisper.build', 'whisper.build']]) expect(await actions.confirm(raw)).toEqual({ ok: false, code: 'LAI-AGT-020' });
    expect(withGrant).not.toHaveBeenCalled();
    expect(start).not.toHaveBeenCalled();
  });

  it('the grant goes in the PIN grant header, never in the body', async () => {
    const calls: Array<{ path: string; options: { json?: unknown; headers?: Record<string, string> } }> = [];
    const api = createEnsureApi({
      request: async (path: string, options: { json?: unknown; headers?: Record<string, string> } = {}) => {
        calls.push({ path, options });
        return new Response(JSON.stringify(view('installing')), { status: 202 });
      },
    } as unknown as Pick<AgentClient, 'request'>);
    await api.start('en', { confirm: ['whisper.build'], grant: GRANT });
    expect(calls[0]!.options.headers).toMatchObject({ [PIN_GRANT_HEADER]: GRANT, 'Accept-Language': 'en' });
    expect(JSON.stringify(calls[0]!.options.json)).not.toContain(GRANT);
    expect(calls[0]!.options.json).toEqual({ confirm: ['whisper.build'] });
  });
});
