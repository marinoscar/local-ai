// `LocalaiStateSnapshotV1` (docs/specs/cloud-app.md section 3.6): the shared fixtures parse, and every bound and
// the `.strict()` rule of the acceptance criteria fails when it should.
import { ai3, lab3, loadFixture } from '../../../test/localai/state-fixtures';
import {
  DrillResultsV1,
  LOCALAI_STATE_BODY_LIMIT_BYTES,
  LocalaiStateResponseSchema,
  LocalaiStateSnapshotV1,
  ReadyReportV1,
} from './state.schema';

const ok = (value: unknown) => LocalaiStateSnapshotV1.safeParse(value).success;

describe('LocalaiStateSnapshotV1', () => {
  it('parses the full ai-n3 fixture', () => {
    const parsed = LocalaiStateSnapshotV1.safeParse(ai3());
    expect(parsed.success).toBe(true);
  });

  it('parses all three snapshots of the 3-node lab (online, offline mode with a battery, serve pattern none)', () => {
    const lab = lab3();
    expect(lab.map((snapshot) => snapshot.node_id)).toEqual(['ai-n1', 'ai-n2', 'ai-n3']);
    for (const snapshot of lab) expect(ok(snapshot)).toBe(true);
    expect(lab[1]).toMatchObject({ mode: 'offline', tailscale: { serve_pattern: 'none' }, hardware: { power: { source: 'battery' } } });
  });

  it('keeps unrelated parsing round-trip stable: parsing the fixture returns the same data', () => {
    expect(LocalaiStateSnapshotV1.parse(ai3())).toEqual(ai3());
  });

  it('the fixture carries the full READY.json of a root and the full drill document', () => {
    const snapshot = ai3();
    expect(ReadyReportV1.safeParse(snapshot.ark.roots[0]!.ready).success).toBe(true);
    expect(snapshot.ark.roots[1]!.ready).toBeNull();
    expect(DrillResultsV1.safeParse(snapshot.drill!.results).success).toBe(true);
  });

  describe('size and shape bounds', () => {
    it('accepts 200 models and refuses 201', () => {
      const snapshot = ai3();
      const model = snapshot.models[0]!;
      snapshot.models = Array.from({ length: 200 }, (_, index) => ({ ...model, id: `model-${index}` }));
      expect(ok(snapshot)).toBe(true);
      snapshot.models = Array.from({ length: 201 }, (_, index) => ({ ...model, id: `model-${index}` }));
      expect(ok(snapshot)).toBe(false);
    });

    it('refuses an unknown key at the top level', () => {
      expect(ok({ ...ai3(), extra: 1 })).toBe(false);
    });

    it.each<[string, (s: ReturnType<typeof ai3>) => unknown]>([
      ['llama', (s) => ((s.llama as Record<string, unknown>).extra = 1)],
      ['hardware', (s) => ((s.hardware as Record<string, unknown>).extra = 1)],
      ['hardware.gpus[]', (s) => ((s.hardware.gpus[0] as Record<string, unknown>).extra = 1)],
      ['hardware.power', (s) => ((s.hardware.power as Record<string, unknown>).extra = 1)],
      ['models[]', (s) => ((s.models[0] as Record<string, unknown>).extra = 1)],
      ['services[]', (s) => ((s.services[0] as Record<string, unknown>).extra = 1)],
      ['ark', (s) => ((s.ark as Record<string, unknown>).extra = 1)],
      ['ark.roots[]', (s) => ((s.ark.roots[0] as Record<string, unknown>).extra = 1)],
      ['ark.roots[].tiers', (s) => ((s.ark.roots[0]!.tiers as Record<string, unknown>).extra = 1)],
      ['ark.roots[].ready', (s) => ((s.ark.roots[0]!.ready as Record<string, unknown>).extra = 1)],
      ['ark.roots[].ready.root (root.path is NOT part of the contract)', (s) => ((s.ark.roots[0]!.ready!.root as Record<string, unknown>).path = 'x')],
      ['ark.roots[].ready.generated_by (node_id is implicit)', (s) => ((s.ark.roots[0]!.ready!.generated_by as Record<string, unknown>).node_id = 'ai-n3')],
      ['ark.roots[].ready.items[]', (s) => ((s.ark.roots[0]!.ready!.items[0] as Record<string, unknown>).extra = 1)],
      ['ark.roots[].ready.problems[]', (s) => ((s.ark.roots[0]!.ready!.problems[0] as Record<string, unknown>).extra = 1)],
      ['sync', (s) => ((s.sync as Record<string, unknown>).extra = 1)],
      ['sync.last_run', (s) => ((s.sync.last_run as Record<string, unknown>).extra = 1)],
      ['drill', (s) => ((s.drill as Record<string, unknown>).extra = 1)],
      ['drill.results (node_id is implicit)', (s) => ((s.drill!.results as Record<string, unknown>).node_id = 'ai-n3')],
      ['drill.results.checks[]', (s) => ((s.drill!.results!.checks[0] as Record<string, unknown>).extra = 1)],
      ['drill.results.restored', (s) => ((s.drill!.results!.restored as Record<string, unknown>).extra = 1)],
      ['drill.results.network_fence', (s) => ((s.drill!.results!.network_fence as Record<string, unknown>).extra = 1)],
      ['tailscale', (s) => ((s.tailscale as Record<string, unknown>).extra = 1)],
      ['alerts[]', (s) => ((s.alerts[0] as Record<string, unknown>).extra = 1)],
    ])('refuses an unknown key in %s', (_where, mutate) => {
      const snapshot = ai3();
      mutate(snapshot);
      expect(ok(snapshot)).toBe(false);
    });

    it.each<[string, (s: ReturnType<typeof ai3>) => void]>([
      ['a node id that is not ai-n<N>', (s) => (s.node_id = 'ai-n0' as never)],
      ['a boot id that is not a uuid', (s) => (s.boot_id = 'not-a-uuid')],
      ['a negative seq', (s) => (s.seq = -1)],
      ['a fractional seq', (s) => (s.seq = 1.5)],
      ['a timestamp without an offset', (s) => (s.reported_at = '2026-11-03T12:00:00')],
      ['an unknown mode', (s) => (s.mode = 'standby' as never)],
      ['a catalog version in the wrong shape', (s) => (s.catalog_version = '2026-11-02')],
      ['an unknown llama backend', (s) => (s.llama.backend = 'metal' as never)],
      ['models_max 0', (s) => (s.llama.models_max = 0)],
      ['a model id with an upper-case letter', (s) => (s.models[0]!.id = 'Qwen3')],
      ['a model with 0 slots', (s) => (s.models[0]!.slots = 0)],
      ['a model with an unknown status', (s) => (s.models[0]!.status = 'paused' as never)],
      ['a root id of the wrong form', (s) => (s.ark.roots[1]!.id = 'ark-XYZ')],
      ['a tier-3 value outside the enum', (s) => (s.ark.roots[0]!.tiers.t1 = 'not_planned' as never)],
      ['a negative byte count', (s) => (s.ark.roots[0]!.bytes_used = -1)],
      ['a power level over 100', (s) => (s.hardware.power.battery_percent = 101)],
      ['a doctor code in the wrong shape', (s) => (s.alerts[0]!.code = 'CLD-029')],
      ['an alert severity of error (the agent maps it to critical)', (s) => (s.alerts[0]!.severity = 'error' as never)],
      ['progress over 100', (s) => (s.sync.progress_pct = 100.5)],
      ['a title over 200 characters', (s) => (s.alerts[0]!.title = 'x'.repeat(201))],
      ['more than 8 GPUs', (s) => (s.hardware.gpus = Array.from({ length: 9 }, () => s.hardware.gpus[0]!))],
      ['more than 9 ark roots', (s) => (s.ark.roots = Array.from({ length: 10 }, () => s.ark.roots[0]!))],
      ['more than 600 READY items', (s) => (s.ark.roots[0]!.ready!.items = Array.from({ length: 601 }, () => s.ark.roots[0]!.ready!.items[0]!))],
      ['more than 100 drill checks', (s) => (s.drill!.results!.checks = Array.from({ length: 101 }, () => s.drill!.results!.checks[0]!))],
      ['a drill detail that is an object', (s) => (s.drill!.results!.checks[0]!.details = { nested: { a: 1 } } as never)],
      ['a drill detail string over 500 characters', (s) => (s.drill!.results!.checks[0]!.details = { text: 'x'.repeat(501) })],
    ])('refuses %s', (_name, mutate) => {
      const snapshot = ai3();
      mutate(snapshot);
      expect(ok(snapshot)).toBe(false);
    });

    it.each(['schema_version', 'node_id', 'boot_id', 'seq', 'mode', 'llama', 'hardware', 'models', 'services', 'ark', 'sync', 'drill', 'tailscale', 'alerts'])(
      'refuses a snapshot without %s',
      (key) => {
        const snapshot = ai3() as unknown as Record<string, unknown>;
        delete snapshot[key];
        expect(ok(snapshot)).toBe(false);
      },
    );

    it('accepts the nullable blocks as null (drill, mode_transition, sync.last_run)', () => {
      const snapshot = ai3();
      snapshot.drill = null;
      snapshot.mode_transition = null;
      snapshot.sync.last_run = null;
      expect(ok(snapshot)).toBe(true);
    });

    it('accepts a mode transition in progress', () => {
      const snapshot = ai3();
      snapshot.mode_transition = { target: 'offline', step: 'stop_sync', started_at: '2026-11-03T12:00:00Z' };
      expect(ok(snapshot)).toBe(true);
    });

    it('accepts a READY.json whose counts list only some statuses', () => {
      const snapshot = ai3();
      snapshot.ark.roots[0]!.ready!.counts = { verified: 3 };
      expect(ok(snapshot)).toBe(true);
      snapshot.ark.roots[0]!.ready!.counts = { verified: 3, bogus: 1 } as never;
      expect(ok(snapshot)).toBe(false);
    });
  });
});

describe('the state response', () => {
  it('the response fixture is {data:{accepted,seq}} and satisfies the strict schema', () => {
    const fixture = loadFixture<{ data: unknown; meta: { timestamp: string } }>('state.response.json');
    expect(LocalaiStateResponseSchema.safeParse(fixture.data).success).toBe(true);
    expect(LocalaiStateResponseSchema.safeParse({ accepted: false, seq: 1 }).success).toBe(false);
    expect(LocalaiStateResponseSchema.safeParse({ accepted: true, seq: 1, extra: 1 }).success).toBe(false);
    expect(typeof fixture.meta.timestamp).toBe('string');
  });
});

describe('the body limit', () => {
  it('is 256 KiB', () => {
    expect(LOCALAI_STATE_BODY_LIMIT_BYTES).toBe(256 * 1024);
  });

  it('the full ai-n3 fixture is far below it', () => {
    expect(Buffer.byteLength(JSON.stringify(ai3()))).toBeLessThan(LOCALAI_STATE_BODY_LIMIT_BYTES / 4);
  });
});
