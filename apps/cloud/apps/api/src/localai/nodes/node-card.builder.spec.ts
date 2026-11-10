// The card builder over the 3-node lab of the wireframe: the output equals the shared fixture the web tests render, and
// the DTO schema accepts it. Plus the cases the fixture does not cover (no snapshot, retired links, drive handling).
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { LAB_3_SOURCES, LAB_CONTEXT, LAB_NOW } from '../../../test/localai/support/lab-3-nodes.inputs';
import { LabNodesResponseSchema, NodeCardDtoSchema } from './dto/node-card';
import { buildNodeCard, compareNodeIds, summarize, type CardSource } from './node-card.builder';

const fixture = JSON.parse(readFileSync(join(__dirname, '../../../test/fixtures/localai/lab-3-nodes.json'), 'utf8')) as unknown;

const cards = LAB_3_SOURCES.map((source) => buildNodeCard(source, LAB_CONTEXT));
const with_ = (index: number, patch: Partial<CardSource['node']>, extra: Partial<CardSource> = {}): CardSource => ({
  ...LAB_3_SOURCES[index]!,
  ...extra,
  node: { ...LAB_3_SOURCES[index]!.node, ...patch },
});

describe('buildNodeCard: the wireframe lab', () => {
  it('equals the shared fixture (lab-3-nodes.json) and satisfies the wire schema', () => {
    const response = { summary: summarize(cards, 0), nodes: cards };
    expect(LabNodesResponseSchema.parse(fixture)).toEqual(fixture);
    expect(response).toEqual(fixture);
  });

  it('ai-n1: healthy, online, routable, 61 of 96 GiB, ark ready, no drive known', () => {
    expect(cards[0]).toMatchObject({
      nodeId: 'ai-n1',
      health: 'healthy',
      mode: 'online',
      routable: true,
      hardware: '128 GB unified',
      backend: 'vulkan',
      loadedModels: [{ id: 'qwen3-coder-next-q4km', isDefault: true }],
      vram: { usedMib: 61 * 1024, totalMib: 96 * 1024 },
      sync: { result: 'ok' },
      drill: { result: 'pass' },
      agentBehind: false,
      catalogBehind: false,
    });
    expect(cards[0]!.ark.map((root) => [root.kind, root.colour, root.label])).toEqual([['internal', 'green', 'ready']]);
  });

  it('ai-n2: hardware from the snapshot, a failed sync turns every root red (spec 4.7), the drive row still shows its 41 days, 2 alerts', () => {
    expect(cards[1]).toMatchObject({ hardware: 'RTX 4080 12 GB', health: 'healthy', routable: true, alerts: { active: 2, critical: 1 }, sync: { result: 'failed' } });
    expect(cards[1]!.ark.map((root) => [root.rootId, root.kind, root.present, root.colour, root.label, root.driveAgeDays])).toEqual([
      ['internal', 'internal', true, 'red', 'sync failed', null],
      ['ark-1a2b3c4d', 'ark_drive', true, 'red', '41 days', 41],
    ]);
  });

  it('ai-n3: quiet (offline mode), not routable, last default kept, unplugged drive 6 days, behind on agent and catalog', () => {
    expect(cards[2]).toMatchObject({
      health: 'quiet',
      mode: 'offline',
      routable: false,
      lastDefault: 'qwen3.5-9b-q4km',
      loadedModels: [],
      agentBehind: true,
      catalogBehind: true,
      sync: { result: 'skipped', skipReason: 'mode' },
    });
    expect(cards[2]!.ark[1]).toMatchObject({ kind: 'ark_drive', present: false, colour: 'green', driveAgeDays: 6, label: 'Drive not plugged in · last synced 6 days ago' });
  });

  it('never carries a tailnet IP, a key hint or the raw snapshot', () => {
    const text = JSON.stringify(fixture);
    expect(text).not.toMatch(/tailnet|keyHint|"lastState"|boot_id|"details"/i);
    for (const card of cards) expect(Object.keys(card)).not.toEqual(expect.arrayContaining(['tailnetIp']));
    for (const card of cards) expect(NodeCardDtoSchema.safeParse(card).success).toBe(true);
  });
});

describe('buildNodeCard: edge cases', () => {
  it('a node that never reported: no snapshot, grey ark row, down when enrolled, quiet when pending', () => {
    const empty = { lastState: null, lastStateAt: null, mode: null, agentVersion: null, catalogVersion: null, keyReportedAt: null, backend: null };
    const enrolled = buildNodeCard(with_(0, empty, { models: [] }), LAB_CONTEXT);
    expect(enrolled).toMatchObject({ health: 'down', mode: null, routable: false, vram: null, hardware: '128 GB unified', lastStateAt: null, lastDefault: null });
    expect(enrolled.ark).toEqual([{ rootId: 'internal', kind: 'internal', present: false, colour: 'grey', label: 'no data yet', driveAgeDays: null }]);
    expect(enrolled.sync).toEqual({ result: null, finishedAt: null, skipReason: null });
    expect(enrolled.drill).toEqual({ result: null });
    const pending = buildNodeCard(with_(0, { ...empty, phase: 'pending' }, { models: [], eab: 'offline' }), LAB_CONTEXT);
    expect(pending.health).toBe('quiet');
  });

  it('a node with no worker row (eab offline) is down even with a fresh snapshot', () => {
    expect(buildNodeCard({ ...LAB_3_SOURCES[0]!, eab: 'offline' }, LAB_CONTEXT).health).toBe('down');
  });

  it('keeps an unplugged drive the agent no longer reports a root for', () => {
    const node = with_(0, { lastState: { ...(LAB_3_SOURCES[0]!.node.lastState as object), ark: { roots: [], drive_last_synced_at: '2026-09-23T12:00:00.000Z' } } });
    const ark = buildNodeCard(node, LAB_CONTEXT).ark;
    expect(ark).toEqual([{ rootId: 'ark-drive', kind: 'ark_drive', present: false, colour: 'amber', label: 'Drive not plugged in · last synced 41 days ago', driveAgeDays: 41 }]);
  });

  describe('the ensure summary ("Install what\'s missing", issue #480)', () => {
    const state = (ensure: unknown) => ({ ...(LAB_3_SOURCES[0]!.node.lastState as object), ensure });
    const block = {
      state: 'needs_you',
      auto: 'on',
      message: 'Needs you',
      progress: { done: 1, total: 3 },
      next_action: { kind: 'confirm', step: 'whisper.build' },
      steps: [{ id: 'whisper.build', status: 'needs_confirm' }],
    };

    it('is null for a snapshot without the block (an agent that predates it): the card still builds', () => {
      expect(LAB_3_SOURCES.map((source) => buildNodeCard(source, LAB_CONTEXT).ensure)).toEqual([null, null, null]);
      expect(buildNodeCard(with_(0, { lastState: null }), LAB_CONTEXT).ensure).toBeNull();
    });

    it('is a summary of the block: codes only, never the sentence or the step list', () => {
      const card = buildNodeCard(with_(0, { lastState: state(block) }), LAB_CONTEXT);
      expect(card.ensure).toEqual({ state: 'needs_you', auto: 'on', done: 1, total: 3, nextKind: 'confirm', nextStep: 'whisper.build' });
      expect(LabNodesResponseSchema.parse({ summary: summarize([card], 0), nodes: [card] }).nodes[0]!.ensure).toEqual(card.ensure);
    });

    it('reads a damaged block without throwing: an unknown state is no block, unknown words fall back', () => {
      expect(buildNodeCard(with_(0, { lastState: state({ ...block, state: 'exploded' }) }), LAB_CONTEXT).ensure).toBeNull();
      expect(buildNodeCard(with_(0, { lastState: state('x') }), LAB_CONTEXT).ensure).toBeNull();
      const card = buildNodeCard(with_(0, { lastState: state({ state: 'complete', auto: 'weird', progress: { done: -4, total: 'x' }, next_action: { kind: 'weird', step: 5 } }) }), LAB_CONTEXT);
      expect(card.ensure).toEqual({ state: 'complete', auto: 'on', done: 0, total: 0, nextKind: 'none', nextStep: null });
    });
  });

  it('puts the default model first among the loaded ones and reads a damaged lastState without throwing', () => {
    const models = [
      { modelId: 'b-model-q4', status: 'loaded', isDefault: false },
      { modelId: 'a-model-q4', status: 'loaded', isDefault: false },
      { modelId: 'z-model-q4', status: 'loaded', isDefault: true },
      { modelId: 'c-model-q4', status: 'failed', isDefault: false },
    ];
    const card = buildNodeCard(with_(0, { lastState: { ark: 'x', hardware: [], sync: 5, drill: { result: 'bogus' } } }, { models }), LAB_CONTEXT);
    expect(card.loadedModels.map((model) => model.id)).toEqual(['z-model-q4', 'a-model-q4', 'b-model-q4']);
    expect(card.vram).toBeNull();
    expect(card.drill.result).toBeNull();
    expect(card.sync.result).toBeNull();
  });

  it('an agent or catalog without a version is never flagged behind', () => {
    const card = buildNodeCard(with_(0, { agentVersion: null, catalogVersion: null }), LAB_CONTEXT);
    expect(card).toMatchObject({ agentBehind: false, catalogBehind: false });
  });
});

describe('summarize', () => {
  it('counts nodes, online, offline mode, alerts (lab-wide included) and routable', () => {
    expect(summarize(cards, 0)).toEqual({ nodes: 3, online: 2, offlineMode: 1, activeAlerts: 2, routable: 2 });
    expect(summarize(cards, 3).activeAlerts).toBe(5);
    expect(summarize([], 0)).toEqual({ nodes: 0, online: 0, offlineMode: 0, activeAlerts: 0, routable: 0 });
  });

  it('a down node with last mode online is not counted as online', () => {
    const down = buildNodeCard({ ...LAB_3_SOURCES[0]!, eab: 'offline' }, LAB_CONTEXT);
    expect(summarize([down], 0)).toMatchObject({ online: 0, routable: 0 });
  });

  it('LAB_NOW is the instant the fixture was computed at', () => {
    expect(LAB_NOW.toISOString()).toBe('2026-11-03T12:00:00.000Z');
  });
});

describe('compareNodeIds', () => {
  it('sorts numerically', () => {
    expect(['ai-n10', 'ai-n2', 'ai-n1', 'weird'].sort(compareNodeIds)).toEqual(['ai-n1', 'ai-n2', 'ai-n10', 'weird']);
  });
});
