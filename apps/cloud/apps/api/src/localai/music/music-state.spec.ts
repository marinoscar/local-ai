// The derived states (E11): a node's music readiness, the workload it last switched to, and a request reconciled from
// its command.
import { activeWorkloadOf, musicReadiness, reconcileRequest, stageOf, workloadsOf, type ReadinessModel } from './music-state';

const NOW = new Date('2026-11-14T12:00:00.000Z');
const fresh = new Date(NOW.getTime() - 30_000);
const online = { phase: 'enrolled', mode: 'online', lastStateAt: fresh };
const music = (partial: Partial<ReadinessModel> = {}): ReadinessModel => ({
  modelId: 'acestep-v15-turbo',
  present: true,
  loadable: true,
  loadableReason: null,
  status: 'unloaded',
  roles: ['music'],
  ...partial,
});
const chat: ReadinessModel = { modelId: 'qwen3.6-35b-a3b-q4km', present: true, loadable: true, loadableReason: null, status: 'loaded', roles: ['chat', 'coding'] };

describe('musicReadiness', () => {
  it.each<[string, Parameters<typeof musicReadiness>[0], ReadinessModel[], string]>([
    ['a loaded music model: ready', online, [chat, music({ status: 'loaded' })], 'ready'],
    ['a music model loading: ready (the page shows it as starting)', online, [music({ status: 'loading' })], 'ready'],
    ['a music model on disk that fits: can_load', online, [chat, music()], 'can_load'],
    ['a music model that does not fit: too_little_vram', online, [music({ loadable: false, loadableReason: 'needs 9000 MiB VRAM, 6144 MiB free' })], 'too_little_vram'],
    ['not loadable with no reason: too_little_vram', online, [music({ loadable: false })], 'too_little_vram'],
    ['not loadable for another reason: no_music', online, [music({ loadable: false, loadableReason: 'engine not installed' })], 'no_music'],
    ['listed but not downloaded: no_music', online, [music({ present: false })], 'no_music'],
    ['no music model: no_music', online, [chat], 'no_music'],
    ['offline mode: offline', { ...online, mode: 'offline' }, [music({ status: 'loaded' })], 'offline'],
    ['survival mode: offline', { ...online, mode: 'survival' }, [music({ status: 'loaded' })], 'offline'],
    ['pending: offline', { ...online, phase: 'pending' }, [music()], 'offline'],
    ['silent for 11 minutes: offline', { ...online, lastStateAt: new Date(NOW.getTime() - 660_000) }, [music({ status: 'loaded' })], 'offline'],
    ['never reported: offline', { ...online, lastStateAt: null }, [music()], 'offline'],
    ['10 minutes exactly: still counted', { ...online, lastStateAt: new Date(NOW.getTime() - 600_000) }, [music()], 'can_load'],
  ])('%s', (_name, node, models, expected) => {
    expect(musicReadiness(node, models, NOW)).toBe(expected);
  });
});

describe('readiness from the workloads block (an agent with workloads.v1)', () => {
  const item = (name: string, fits = true, missing: string[] = []) => ({ name, label: { en: name, es: name }, models: ['acestep-v15-turbo'], fits, missing });
  const block = (active: string | null, ...items: ReturnType<typeof item>[]) => ({ active, items });

  it.each<[string, ReturnType<typeof block>, string]>([
    ['active music workload: ready', block('music', item('coding'), item('music')), 'ready'],
    ['a music workload that fits: can_load', block('coding', item('coding'), item('music')), 'can_load'],
    ['a music workload that does not fit: too_little_vram', block('coding', item('music', false)), 'too_little_vram'],
    ['a music workload with missing models: no_music', block(null, item('music', true, ['acestep-v15-turbo'])), 'no_music'],
    ['no music workload: no_music', block('coding', item('coding'), item('chat')), 'no_music'],
  ])('%s', (_name, workloads, expected) => {
    // The block wins over the model rows (here a loaded music model that the block does not list).
    expect(musicReadiness(online, [music({ status: 'loaded' })], NOW, workloads)).toBe(expected);
  });

  it('an offline node is offline whatever its block says', () => {
    expect(musicReadiness({ ...online, mode: 'offline' }, [], NOW, block('music', item('music')))).toBe('offline');
  });

  it('workloadsOf reads the block strictly from the stored snapshot, and null otherwise', () => {
    expect(workloadsOf({ workloads: block('music', item('music')) })).toEqual(block('music', item('music')));
    expect(workloadsOf({ workloads: { active: 'music', items: [], extra: 1 } })).toBeNull();
    expect(workloadsOf({})).toBeNull();
    expect(workloadsOf(null)).toBeNull();
  });

  it('activeWorkloadOf reads a workload.activate result, and nothing from anything else', () => {
    expect(activeWorkloadOf({ active: 'music' })).toBe('music');
    expect(activeWorkloadOf({ active: 'music', extra: 1 })).toBeNull();
    expect(activeWorkloadOf(null)).toBeNull();
  });
});

describe('reconcileRequest', () => {
  const sha = (digit: string) => digit.repeat(64);

  it('queued and running follow the command', () => {
    expect(reconcileRequest({ status: 'queued', result: null, error: null })).toEqual({ status: 'queued', ready: [], error: null });
    expect(reconcileRequest({ status: 'running', result: null, error: null })).toEqual({ status: 'running', ready: [], error: null });
  });

  it('succeeded marks the songs of the result ready', () => {
    const result = { songs: [{ index: 1, sha256: sha('a'), size_bytes: 10, mime: 'audio/wav', duration_s: 59.9 }] };
    expect(reconcileRequest({ status: 'succeeded', result, error: null })).toEqual({
      status: 'succeeded',
      ready: [{ index: 1, sha256: sha('a'), sizeBytes: 10, mime: 'audio/wav', durationS: 59.9 }],
      error: null,
    });
  });

  it('a result it cannot read, or with no song, fails the request instead of inventing songs', () => {
    expect(reconcileRequest({ status: 'succeeded', result: { songs: [{ index: 9 }] }, error: null })).toMatchObject({ status: 'failed', ready: [] });
    expect(reconcileRequest({ status: 'succeeded', result: { songs: [] }, error: null })).toMatchObject({ status: 'failed', ready: [], error: { message: 'The computer finished without a song.' } });
  });

  it('failed keeps the node\'s doctor code and message; expired explains itself', () => {
    expect(reconcileRequest({ status: 'failed', result: null, error: { code: 'LAI-SVC-151', message: 'Music is not loaded' } })).toEqual({
      status: 'failed',
      ready: [],
      error: { code: 'LAI-SVC-151', message: 'Music is not loaded' },
    });
    const expired = reconcileRequest({ status: 'expired', result: null, error: null });
    expect(expired.status).toBe('expired');
    expect(expired.error?.message).toMatch(/did not pick up/);
  });

  it('cancelled is cancelled', () => {
    expect(reconcileRequest({ status: 'cancelled', result: null, error: null }).status).toBe('cancelled');
  });

  it('the stage the page shows', () => {
    expect(['queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled'].map((status) => stageOf(status as never))).toEqual(['queued', 'generating', 'done', 'failed', 'expired', 'cancelled']);
  });
});
