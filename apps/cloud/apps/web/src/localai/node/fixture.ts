// The node detail fixture: ai-n2 of the shared 3-node lab fixture plus what the detail route adds (the last snapshot, the
// settings, the key hint, the accepted command types, planned sizes). Invented names and values only; the tailnet
// address is built from parts so no literal address sits in the repository.
import type { CommandView, NodeDetail } from '../api/types';
import { LAB_NOW, cardOf } from '../lab/fixture';

export { LAB_NOW };

/** A made-up tailnet-shaped value, built from parts. */
export const FAKE_TAILNET_IP = [100, 90, 1, 2].join('.');
export const MODEL_LOADED = 'qwen3.6-35b-a3b-q4km';
export const MODEL_READY = 'qwen3.5-9b-q4km';
export const MODEL_ABSENT = 'small-vlm-id';
export const MODEL_TOO_BIG = 'qwen3-coder-next-q4km';

export const ALL_TYPES = [
  'status.refresh', 'model.load', 'model.unload', 'model.set_default', 'model.download', 'model.delete', 'ark.sync', 'ark.verify',
  'mode.offline', 'key.rotate', 'service.restart', 'logs.tail', 'catalog.refresh', 'agent.update', 'ensure.run',
];

export function detailFixture(patch: Partial<NodeDetail> = {}): NodeDetail {
  const card = cardOf('ai-n2');
  const base: NodeDetail = {
    ...card,
    lastState: {
      mode: 'online',
      llama: { build: 'f3586e66b-cuda', backend: 'cuda', router: 'up', models_max: 1 },
      hardware: { gpus: [{ name: 'RTX 4080', vram_total_mib: 12288, vram_used_mib: 11469 }] },
      models: [
        { id: MODEL_LOADED, present: true, verified: true, loadable: true, loadable_reason: null, status: 'loaded', default: true, roots: ['internal'] },
        { id: MODEL_READY, present: true, verified: true, loadable: true, loadable_reason: null, status: 'unloaded', default: false, roots: ['internal', 'ark-1a2b3c4d'] },
        { id: MODEL_ABSENT, present: false, verified: false, loadable: true, loadable_reason: null, status: 'unloaded', default: false, roots: [] },
        { id: MODEL_TOO_BIG, present: false, verified: false, loadable: false, loadable_reason: 'needs 49 GB', status: 'unloaded', default: false, roots: [] },
      ],
      services: [
        { name: 'llama', active_state: 'active', since: '2026-11-03T09:00:00.000Z' },
        { name: 'kiwix', active_state: 'active', since: '2026-11-03T09:00:00.000Z' },
        { name: 'whisper', active_state: 'failed', since: '2026-11-03T11:00:00.000Z' },
        { name: 'sync', active_state: 'inactive', since: null },
        { name: 'drill', active_state: 'inactive', since: null },
      ],
      ark: {
        drive_last_synced_at: '2026-09-23T10:00:00.000Z',
        roots: [
          { id: 'internal', present: true, tiers: { t1: 'ok', t2: 'partial', t3: 'not_planned' }, items_missing: 3, items_failed: 1, items_skipped_budget: 2, bytes_used: 400_000_000_000, bytes_free: 100_000_000_000, last_sync_at: '2026-11-03T02:14:00.000Z', last_verify_at: '2026-11-02T03:00:00.000Z' },
          { id: 'ark-1a2b3c4d', present: true, tiers: { t1: 'ok', t2: 'ok', t3: 'missing' }, items_missing: 0, items_failed: 0, items_skipped_budget: 0, bytes_used: 200_000_000_000, bytes_free: 800_000_000_000, last_sync_at: '2026-09-23T10:00:00.000Z', last_verify_at: null },
        ],
      },
      sync: { running: false, progress_pct: null, last_run: { id: 'run-1', finished_at: '2026-11-03T02:14:00.000Z', result: 'failed', skip_reason: null, error_code: 'LAI-SYN-031' } },
    },
    settings: {
      publicHost: 'ai-n2.example.test',
      servePattern: 'B',
      tailnetMachine: 'machine-two',
      tailnetIpSet: true,
      gatewayEnabled: true,
      gatewayPriority: 100,
      catalogPin: null,
      profile: 'cuda-12gb',
      connectivity: 'full',
    },
    keys: {
      keyHint: 'ab12…9f0e',
      keyReportedAt: '2026-11-03T08:00:00.000Z',
      credential: { prefix: 'Xy7Kq2', lastUsedAt: '2026-11-03T11:59:30.000Z', createdAt: '2026-10-01T00:00:00.000Z' },
    },
    acceptedTypes: [...ALL_TYPES],
    modelSizes: { [MODEL_ABSENT]: 5_500_000_000, [MODEL_READY]: 5_868_826_976 },
    agentLatestVersion: null,
  };
  return { ...base, ...patch };
}

let counter = 0;
export function commandFixture(patch: Partial<CommandView> = {}): CommandView {
  counter += 1;
  return {
    id: `00000000-0000-4000-8000-${String(counter).padStart(12, '0')}`,
    nodeId: 'ai-n2',
    type: 'localai.model.load',
    status: 'queued',
    params: { model_id: MODEL_READY, wait: true, unload_others: true, set_default: false },
    source: 'ui',
    expiresAt: '2026-11-03T12:15:00.000Z',
    createdAt: '2026-11-03T12:00:00.000Z',
    claimedAt: null,
    finishedAt: null,
    error: null,
    ...patch,
  };
}
