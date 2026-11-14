// Issue #515: the Lab's workload buttons and "Make music on this node" (src/renderer/lab/model.ts), the readers of the
// cloud's answers (src/shared/lab-data.ts) and the form check. The controller only carries the ask; the tests prove it
// asks exactly the allowlisted routes.
import { describe, expect, it, vi } from 'vitest';
import { decideCloudRequest } from '../../src/main/cloud-api';
import {
  COMMAND_POLL_MS,
  type Clock,
  LabController,
  type LabBridge,
  MUSIC_FORM_DEFAULTS,
  SENT_REFRESH_MS,
  musicInputFrom,
  musicStatusText,
  musicWorkloadInactive,
  refusalText,
} from '../../src/renderer/lab/model';
import { STRINGS } from '../../src/renderer/lab/strings';
import { projectMusicRequest, projectSongs, projectWorkloads } from '../../src/shared/lab-data';
import type { CloudResult, DesktopStatus } from '../../src/shared/types';
import fixture from '../fixtures/lab-3-nodes.json';

const COMMAND_ID = '6b3c1f0e-4a2d-4c8b-9e51-0a7d2b9c3e44';
const REQUEST_ID = '0d9e8f7a-1b2c-4d3e-8f90-123456789abc';
const SONG_A = '11111111-2222-4333-8444-555555555555';
const SONG_B = '11111111-2222-4333-8444-666666666666';

class FakeClock implements Clock {
  time = Date.parse('2026-11-03T14:05:00.000Z');
  #next = 1;
  readonly timers = new Map<number, { at: number; fn: () => void }>();
  now = (): number => this.time;
  set = (fn: () => void, ms: number): unknown => {
    this.timers.set(this.#next, { at: this.time + ms, fn });
    return this.#next++;
  };
  clear = (handle: unknown): void => void this.timers.delete(handle as number);
  delays(): number[] {
    return [...this.timers.values()].map((t) => t.at - this.time).sort((a, b) => a - b);
  }
  async advance(ms: number): Promise<void> {
    const end = this.time + ms;
    for (;;) {
      const due = [...this.timers.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      this.timers.delete(due[0]);
      this.time = due[1].at;
      due[1].fn();
      await flush();
    }
    this.time = end;
  }
}

const flush = async (): Promise<void> => {
  for (let i = 0; i < 20; i += 1) await Promise.resolve();
};

const status = (): DesktopStatus => ({
  version: '0.1.0',
  agent: 'up',
  mode: 'online',
  connectivity: 'full',
  alerts: 0,
  cloud: { linked: true, tokenHeld: true, url: 'https://ai.marin.cr', user: 'Person A' },
  connected: true,
  tab: 'lab',
});

const okNodes = (): CloudResult => ({ ok: true, status: 200, body: { data: fixture } });
const nodeDetail = (workloads: unknown): CloudResult => ({ ok: true, status: 200, body: { data: { nodeId: 'ai-n2', lastState: { models: [], workloads } } } });
const agentWorkloads = {
  workloads: [
    { name: 'coding', label: { en: 'Coding', es: 'Programación' }, models: ['m1'], active: true, fits: true, missing: [] },
    { name: 'music', label: { en: 'Music', es: 'Música' }, models: ['m2'], active: false, fits: false, missing: ['m2'] },
  ],
  active: 'coding',
};

type Req = { method: 'GET' | 'POST' | 'DELETE'; path: string; body?: unknown };

async function setup(handler: (req: Req) => CloudResult | undefined) {
  const clock = new FakeClock();
  const request = vi.fn(async (req: Req) => {
    // Every request the controller makes must pass the real allowlist: the page cannot ask for anything else.
    expect(decideCloudRequest(req).allow, `${req.method} ${req.path}`).toBe(true);
    return handler(req) ?? okNodes();
  });
  const bridge: LabBridge = { status: async () => status(), onStatus: () => () => undefined, cloud: { request, cached: vi.fn(async () => null), openNode: vi.fn() } };
  const controller = new LabController(bridge, 'en', clock);
  await controller.start();
  await flush();
  request.mockClear();
  const calls = (): string[] => request.mock.calls.map(([r]) => `${r.method} ${r.path}`);
  return { controller, clock, request, calls, state: () => controller.getState() };
}

describe('reading the cloud', () => {
  it('projectWorkloads reads the agent report from the node (block or bare list), name-checked and bounded', () => {
    expect(projectWorkloads(nodeDetail(agentWorkloads).ok ? (nodeDetail(agentWorkloads) as { body: unknown }).body : null)).toEqual([
      { name: 'coding', label: { en: 'Coding', es: 'Programación' }, active: true, fits: true, missing: 0 },
      { name: 'music', label: { en: 'Music', es: 'Música' }, active: false, fits: false, missing: 1 },
    ]);
    expect(projectWorkloads({ data: { lastState: { workloads: [{ name: 'chat', active: true }] } } })).toEqual([{ name: 'chat', label: { en: 'chat', es: 'chat' }, active: true, fits: true, missing: 0 }]);
    // `active` named at the block level counts too
    expect(projectWorkloads({ data: { lastState: { workloads: { workloads: [{ name: 'music' }], active: 'music' } } } })?.[0]?.active).toBe(true);
    expect(projectWorkloads({ data: { lastState: { workloads: [{ name: '../x' }, { name: 'Bad' }, { name: 7 }, null] } } })).toEqual([]);
    expect(projectWorkloads({ data: { lastState: {} } })).toEqual([]);
    expect(projectWorkloads({ data: {} })).toEqual([]);
    expect(projectWorkloads({})).toBeNull();
    expect(projectWorkloads({ data: { lastState: { workloads: Array.from({ length: 40 }, (_, i) => ({ name: `w${i}` })) } } })).toHaveLength(16);
  });

  it('projectMusicRequest: a UUID id and a known state (aliases read as command states), codes checked', () => {
    expect(projectMusicRequest({ data: { id: REQUEST_ID, status: 'queued' } })).toEqual({ id: REQUEST_ID, status: 'queued', errorCode: null, errorMessage: null });
    expect(projectMusicRequest({ data: { id: REQUEST_ID, status: 'done' } })?.status).toBe('succeeded');
    expect(projectMusicRequest({ data: { id: REQUEST_ID, status: 'failed', error: { code: 'LAI-SVC-152', message: 'The music engine stopped' } } })).toEqual({
      id: REQUEST_ID,
      status: 'failed',
      errorCode: 'LAI-SVC-152',
      errorMessage: 'The music engine stopped',
    });
    expect(projectMusicRequest({ data: { id: REQUEST_ID, status: 'failed', error: { code: 'not-a-code' } } })?.errorCode).toBeNull();
    expect(projectMusicRequest({ data: { id: 'x', status: 'queued' } })).toBeNull();
    expect(projectMusicRequest({ data: { id: REQUEST_ID, status: 'exploded' } })).toBeNull();
    expect(projectMusicRequest({ id: REQUEST_ID, status: 'queued' })).toBeNull();
  });

  it('projectSongs: a list or a paged object, ids checked, titles bounded, no secret-shaped text', () => {
    const songs = projectSongs({
      data: {
        items: [
          { id: SONG_A, nodeId: 'ai-n2', title: 'Line one', duration_s: 61.4, created_at: '2026-11-03T14:00:00Z' },
          { id: SONG_B, nodeId: 'ai-n3', tags: 'folk', durationS: 30, createdAt: 'yesterday' },
          { id: 'nope', title: 'x' },
          { id: SONG_A, nodeId: 'ai-n2', title: 'Bearer abcdefghijklmnop' },
        ],
      },
    });
    expect(songs).toEqual([
      { id: SONG_A, nodeId: 'ai-n2', title: 'Line one', durationS: 61, createdAt: '2026-11-03T14:00:00Z' },
      { id: SONG_B, nodeId: 'ai-n3', title: 'folk', durationS: 30, createdAt: null },
      { id: SONG_A, nodeId: 'ai-n2', title: '', durationS: null, createdAt: null },
    ]);
    expect(projectSongs({ data: [] })).toEqual([]);
    expect(projectSongs({ data: {} })).toBeNull();
    expect(projectSongs(null)).toBeNull();
  });
});

describe('workload buttons', () => {
  it('the Workloads panel reads the node once (no list route) and shows its workloads', async () => {
    const t = await setup((req) => (req.path === '/api/localai/nodes/ai-n2' ? nodeDetail(agentWorkloads) : undefined));
    t.controller.toggleWorkloads('ai-n2');
    expect(t.state().workloads['ai-n2']).toMatchObject({ open: true, state: 'loading' });
    await flush();
    expect(t.calls()).toEqual(['GET /api/localai/nodes/ai-n2']);
    expect(t.state().workloads['ai-n2']).toMatchObject({ open: true, state: 'ready' });
    expect(t.state().workloads['ai-n2']!.workloads.map((w) => [w.name, w.active])).toEqual([
      ['coding', true],
      ['music', false],
    ]);
    t.controller.toggleWorkloads('ai-n2');
    expect(t.state().workloads['ai-n2']!.open).toBe(false);
  });

  it('a node that cannot be read shows the error state', async () => {
    const t = await setup((req) => (req.path === '/api/localai/nodes/ai-n2' ? { ok: false, status: 500, body: null } : undefined));
    t.controller.toggleWorkloads('ai-n2');
    await flush();
    expect(t.state().workloads['ai-n2']!.state).toBe('error');
  });

  it('a workload button POSTs the activate route with an empty body, then follows the command it gets', async () => {
    let reads = 0;
    const t = await setup((req) => {
      if (req.method === 'POST') return { ok: true, status: 202, body: { data: { id: COMMAND_ID, nodeId: 'ai-n2', type: 'localai.workload.activate', status: 'queued' } } };
      if (req.path === `/api/localai/commands/${COMMAND_ID}`) return { ok: true, status: 200, body: { data: { id: COMMAND_ID, nodeId: 'ai-n2', status: reads++ === 0 ? 'running' : 'succeeded' } } };
      if (req.path === '/api/localai/nodes/ai-n2') return nodeDetail(agentWorkloads);
      return undefined;
    });
    t.controller.toggleWorkloads('ai-n2');
    await flush();
    await t.controller.activateWorkload('ai-n2', 'music');
    expect(t.request).toHaveBeenCalledWith({ method: 'POST', path: '/api/localai/nodes/ai-n2/workloads/music/activate', body: {} });
    expect(t.state().commands['ai-n2']).toMatchObject({ status: 'queued', id: COMMAND_ID });
    expect(t.controller.isBusy('ai-n2')).toBe(true);
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().commands['ai-n2']!.status).toBe('running');
    t.request.mockClear();
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().commands['ai-n2']!.status).toBe('succeeded');
    // settled: the open panel and the cards are read again
    expect(t.calls()).toEqual([`GET /api/localai/commands/${COMMAND_ID}`, 'GET /api/localai/nodes/ai-n2', 'GET /api/localai/nodes']);
  });

  it('a 202 with nothing to follow says "Sent" and reads the cards again shortly', async () => {
    const t = await setup((req) => (req.method === 'POST' ? { ok: true, status: 202, body: { data: { accepted: true } } } : undefined));
    await t.controller.activateWorkload('ai-n1', 'coding');
    expect(t.state().commands['ai-n1']).toMatchObject({ status: 'sent', text: STRINGS.sent.en });
    expect(t.controller.isBusy('ai-n1')).toBe(false);
    expect(t.clock.delays()).toContain(SENT_REFRESH_MS);
  });

  it('a name that is not a workload name is never sent', async () => {
    const t = await setup(() => undefined);
    for (const bad of ['../commands', 'Music', '', 'a/b', '1x']) await t.controller.activateWorkload('ai-n1', bad);
    expect(t.request).not.toHaveBeenCalled();
  });

  it('the node\'s refusals have their own words and codes, in both languages', () => {
    const refusal = (code: string, status = 409): CloudResult => ({ ok: false, status, body: { error: { code, message: 'refused' } } });
    expect(refusalText(refusal('LAI-LLM-050', 404), 'en')).toBe(STRINGS.refusedWorkloadUnknown.en);
    expect(refusalText(refusal('LAI-LLM-051', 422), 'es')).toBe(STRINGS.refusedWorkloadFit.es);
    expect(refusalText(refusal('LAI-LLM-053'), 'en')).toContain('LAI-LLM-053');
    expect(refusalText(refusal('LAI-SVC-151'), 'es')).toContain('LAI-SVC-151');
    expect(refusalText(refusal('LAI-SVC-153', 429), 'en')).toContain('LAI-SVC-153');
    expect(refusalText(refusal('LAI-SVC-154', 413), 'en')).toContain('LAI-SVC-154');
  });
});

describe('Make music on this node', () => {
  const input = { lyrics: 'Line one', tags: 'folk', durationS: 60, language: 'es' as const, variations: 2 };

  it('POSTs the music request with the node, follows it every 2 s, and reads the node\'s songs when it is done', async () => {
    let reads = 0;
    const t = await setup((req) => {
      if (req.method === 'POST') return { ok: true, status: 202, body: { data: { id: REQUEST_ID, status: 'queued' } } };
      if (req.path === `/api/localai/music/requests/${REQUEST_ID}`) return { ok: true, status: 200, body: { data: { id: REQUEST_ID, status: ['running', 'succeeded'][Math.min(reads++, 1)] } } };
      if (req.path === '/api/localai/music/songs') return { ok: true, status: 200, body: { data: [{ id: SONG_A, nodeId: 'ai-n2', title: 'Line one' }, { id: SONG_B, nodeId: 'ai-n3', title: 'Other node' }] } };
      return undefined;
    });
    await t.controller.loadSongs('ai-n2');
    expect(t.state().songs['ai-n2']).toEqual({ state: 'ready', songs: [{ id: SONG_A, nodeId: 'ai-n2', title: 'Line one', durationS: null, createdAt: null }] });
    t.request.mockClear();

    await t.controller.requestMusic('ai-n2', input);
    expect(t.request).toHaveBeenCalledWith({
      method: 'POST',
      path: '/api/localai/music/requests',
      body: { nodeId: 'ai-n2', lyrics: 'Line one', tags: 'folk', duration_s: 60, language: 'es', variations: 2 },
    });
    expect(t.state().music['ai-n2']).toMatchObject({ status: 'queued', id: REQUEST_ID, text: STRINGS.musicQueued.en });
    expect(t.controller.isMusicBusy('ai-n2')).toBe(true);
    // A second request while one runs is ignored.
    await t.controller.requestMusic('ai-n2', input);
    expect(t.calls().filter((c) => c.startsWith('POST'))).toHaveLength(1);

    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().music['ai-n2']).toMatchObject({ status: 'running', text: STRINGS.musicRunning.en });
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().music['ai-n2']).toMatchObject({ status: 'succeeded', text: STRINGS.musicDone.en });
    expect(t.controller.isMusicBusy('ai-n2')).toBe(false);
    expect(t.calls()).toContain('GET /api/localai/music/songs');
    t.request.mockClear();
    await t.clock.advance(COMMAND_POLL_MS * 3);
    expect(t.calls().filter((c) => c.includes('/music/requests/'))).toEqual([]);
  });

  it('a refusal shows the node\'s reason; a refused allowlist call sends nothing more', async () => {
    const t = await setup((req) => (req.method === 'POST' ? { ok: false, status: 409, body: { error: { details: { code: 'LAI-SVC-151' } } } } : undefined));
    await t.controller.requestMusic('ai-n2', input);
    expect(t.state().music['ai-n2']).toMatchObject({ status: 'refused', text: STRINGS.refusedMusicNotLoaded.en });
    expect(t.controller.isMusicBusy('ai-n2')).toBe(false);
  });

  it('a request that fails on the node shows its code; a cancelled one says LAI-SVC-158', () => {
    expect(musicStatusText({ status: 'failed', errorCode: 'LAI-SVC-155', errorMessage: 'Not enough disk space' }, 'en')).toBe('Failed: Not enough disk space (LAI-SVC-155)');
    expect(musicStatusText({ status: 'cancelled', errorCode: null, errorMessage: null }, 'es')).toBe(`${STRINGS.musicCancelled.es} (LAI-SVC-158)`);
    expect(musicStatusText({ status: 'expired', errorCode: null, errorMessage: null }, 'en')).toContain('LAI-CLD-030');
  });

  it('a poll that cannot be read stops asking and lets the node list find out', async () => {
    const t = await setup((req) => {
      if (req.method === 'POST') return { ok: true, status: 202, body: { data: { id: REQUEST_ID, status: 'queued' } } };
      if (req.path === `/api/localai/music/requests/${REQUEST_ID}`) return { ok: false, status: 502, body: null };
      return undefined;
    });
    await t.controller.requestMusic('ai-n2', input);
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().music['ai-n2']!.stalled).toBe(true);
    t.request.mockClear();
    await t.clock.advance(COMMAND_POLL_MS * 2);
    expect(t.calls().filter((c) => c.includes('/music/requests/'))).toEqual([]);
  });
});

describe('the music form', () => {
  it('needs lyrics or a style; checks the two size limits; brings the numbers into range', () => {
    expect(musicInputFrom(MUSIC_FORM_DEFAULTS)).toEqual({ ok: false, problem: 'musicNeedsText' });
    expect(musicInputFrom({ ...MUSIC_FORM_DEFAULTS, lyrics: 'ñ'.repeat(4097) })).toEqual({ ok: false, problem: 'musicTooLong' });
    expect(musicInputFrom({ ...MUSIC_FORM_DEFAULTS, tags: 't'.repeat(513) })).toEqual({ ok: false, problem: 'musicTagsTooLong' });
    expect(musicInputFrom({ lyrics: 'la', tags: ' folk ', duration: '500', language: 'es', variations: '9' })).toEqual({
      ok: true,
      input: { lyrics: 'la', tags: 'folk', durationS: 240, language: 'es', variations: 4 },
    });
    expect(musicInputFrom({ lyrics: '', tags: 'calm', duration: 'abc', language: 'en', variations: '0' })).toEqual({
      ok: true,
      input: { lyrics: '', tags: 'calm', durationS: 60, language: 'en', variations: 1 },
    });
  });

  it('every form value it makes passes the allowlist', () => {
    const checked = musicInputFrom({ lyrics: 'a'.repeat(8192), tags: 't'.repeat(512), duration: '5', language: 'es', variations: '4' });
    expect(checked.ok).toBe(true);
    if (!checked.ok) return;
    const i = checked.input;
    expect(decideCloudRequest({ method: 'POST', path: '/api/localai/music/requests', body: { nodeId: 'ai-n1', lyrics: i.lyrics, tags: i.tags, duration_s: i.durationS, language: i.language, variations: i.variations } }).allow).toBe(true);
  });

  it('the "switch to Music first" hint appears only when the node is known to be on another workload', () => {
    const panel = (workloads: Array<{ name: string; active: boolean }>) => ({ open: true, state: 'ready' as const, workloads: workloads.map((w) => ({ ...w, label: { en: w.name, es: w.name }, fits: true, missing: 0 })) });
    expect(musicWorkloadInactive(undefined)).toBe(false);
    expect(musicWorkloadInactive({ open: true, state: 'loading', workloads: [] })).toBe(false);
    expect(musicWorkloadInactive(panel([{ name: 'coding', active: true }]))).toBe(false);
    expect(musicWorkloadInactive(panel([{ name: 'coding', active: true }, { name: 'music', active: false }]))).toBe(true);
    expect(musicWorkloadInactive(panel([{ name: 'music', active: true }]))).toBe(false);
  });

  it('every new Lab string exists in both languages', () => {
    for (const [key, value] of Object.entries(STRINGS)) {
      expect(value.en, key).not.toBe('');
      expect(value.es, key).not.toBe('');
    }
  });
});
