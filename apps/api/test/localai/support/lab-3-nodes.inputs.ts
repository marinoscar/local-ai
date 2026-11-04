// The three-node lab of the Lab overview wireframe (docs/specs/cloud-app.md section 3.11) as STORED ROWS, the input of
// `buildNodeCard`. The expected output is `test/fixtures/localai/lab-3-nodes.json`, which the web tests render too.
// Node ids and names are invented; no address, token or real host appears.
import type { CardContext, CardSource } from '../../../src/localai/nodes/node-card.builder';

/** The instant every expectation is computed at. */
export const LAB_NOW = new Date('2026-11-03T12:00:00.000Z');

const secondsAgo = (seconds: number) => new Date(LAB_NOW.getTime() - seconds * 1000);
const daysAgo = (days: number) => new Date(LAB_NOW.getTime() - days * 86_400_000);
const iso = (date: Date) => date.toISOString();

const GIB = 1024;

function gpu(name: string, totalGiB: number, usedMib: number) {
  return { name, vram_total_mib: totalGiB * GIB, vram_used_mib: usedMib, temp_c: null };
}

function internalRoot(patch: { t2?: 'ok' | 'partial' | 'missing' } = {}) {
  return {
    id: 'internal',
    present: true,
    ready_generated_at: null,
    tiers: { t1: 'ok', t2: patch.t2 ?? 'ok', t3: 'ok' },
    items_missing: 0,
    items_failed: 0,
    items_skipped_budget: 0,
    bytes_used: 90_000_000_000,
    bytes_free: 900_000_000_000,
    last_sync_at: null,
    last_verify_at: null,
    overall: 'green',
    survival_capable: true,
    ready: null,
  };
}

function driveRoot(present: boolean) {
  return { ...internalRoot(), id: 'ark-1a2b3c4d', present };
}

export const LAB_CONTEXT: CardContext = {
  now: LAB_NOW,
  thresholds: { driveStaleDays: 30, driveCriticalDays: 90 },
  agentLatestVersion: '0.4.1',
  approvedCatalogVersion: '2026.11.02-1',
};

/** ai-n1: healthy and online, 61 of 96 GiB in use, no ark drive known, last sync ok, drill passed. */
const ai1: CardSource = {
  eab: 'healthy',
  alerts: { active: 0, critical: 0 },
  models: [
    { modelId: 'qwen3-coder-next-q4km', status: 'loaded', isDefault: true },
    { modelId: 'qwen3.5-9b-q4km', status: 'unloaded', isDefault: false },
  ],
  node: {
    nodeId: 'ai-n1',
    displayName: 'MS-S1 Max',
    hardware: '128 GB unified',
    backend: 'vulkan',
    phase: 'enrolled',
    mode: 'online',
    servePattern: 'B',
    gatewayEnabled: true,
    agentVersion: '0.4.1',
    catalogVersion: '2026.11.02-1',
    keyReportedAt: daysAgo(2),
    lastStateAt: secondsAgo(8),
    lastState: {
      mode: 'online',
      hardware: { gpus: [gpu('Integrated GPU (Vulkan)', 96, 61 * GIB)] },
      ark: { roots: [internalRoot()], drive_last_synced_at: null },
      sync: { last_run: { id: 'run-1', finished_at: iso(new Date('2026-11-03T02:14:00Z')), result: 'ok', skip_reason: null, error_code: null } },
      drill: { result: 'pass' },
    },
  },
};

/** ai-n2: healthy and online, VRAM nearly full, last sync failed, ark drive plugged in and 41 days old, 2 alerts. */
const ai2: CardSource = {
  eab: 'healthy',
  alerts: { active: 2, critical: 1 },
  models: [
    { modelId: 'qwen3.6-35b-a3b-q4km', status: 'loaded', isDefault: true },
    { modelId: 'qwen3.5-9b-q4km', status: 'unloaded', isDefault: false },
  ],
  node: {
    nodeId: 'ai-n2',
    displayName: 'MSI Stealth',
    hardware: null,
    backend: 'cuda',
    phase: 'enrolled',
    mode: 'online',
    servePattern: 'A',
    gatewayEnabled: true,
    agentVersion: '0.4.1',
    catalogVersion: '2026.11.02-1',
    keyReportedAt: daysAgo(2),
    lastStateAt: secondsAgo(21),
    lastState: {
      mode: 'online',
      hardware: { gpus: [gpu('NVIDIA GeForce RTX 4080', 12, 11_469)] },
      ark: { roots: [internalRoot({ t2: 'partial' }), driveRoot(true)], drive_last_synced_at: iso(daysAgo(41)) },
      sync: { last_run: { id: 'run-2', finished_at: iso(new Date('2026-11-03T02:14:00Z')), result: 'failed', skip_reason: null, error_code: 'LAI-SYN-031' } },
      drill: { result: 'pass' },
    },
  },
};

/** ai-n3: in Offline mode (expected silence) for 3 hours, drive unplugged and 6 days old, behind on agent and catalog. */
const ai3: CardSource = {
  eab: 'offline',
  alerts: { active: 0, critical: 0 },
  models: [
    { modelId: 'qwen3.5-9b-q4km', status: 'unloaded', isDefault: true },
    { modelId: 'qwen3-embed-q8', status: 'unloaded', isDefault: false },
  ],
  node: {
    nodeId: 'ai-n3',
    displayName: 'HP ZBook',
    hardware: null,
    backend: 'cuda',
    phase: 'enrolled',
    mode: 'offline',
    servePattern: 'B',
    gatewayEnabled: true,
    agentVersion: '0.4.0',
    catalogVersion: '2026.10.28-2',
    keyReportedAt: daysAgo(30),
    lastStateAt: secondsAgo(3 * 3600),
    lastState: {
      mode: 'offline',
      hardware: { gpus: [gpu('NVIDIA GeForce RTX 2070', 8, 0)] },
      ark: { roots: [internalRoot(), driveRoot(false)], drive_last_synced_at: iso(daysAgo(6)) },
      sync: { last_run: { id: 'run-3', finished_at: iso(new Date('2026-11-03T08:50:00Z')), result: 'skipped', skip_reason: 'mode', error_code: null } },
      drill: { result: 'pass' },
    },
  },
};

export const LAB_3_SOURCES: readonly CardSource[] = [ai1, ai2, ai3];
