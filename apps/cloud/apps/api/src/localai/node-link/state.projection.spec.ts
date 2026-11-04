// The pure parts of state ingestion: ordering, the READY carry over and the models diff (spec section 4.3).
import { ai3 } from '../../../test/localai/state-fixtures';
import { carryOverReady, diffModels, hasModeChanged, isFreshSnapshot, toModelFields, type ModelRow } from './state.projection';

const BOOT_A = '5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f';
const BOOT_B = '5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e60';

describe('isFreshSnapshot: ordering by (boot_id, seq)', () => {
  const stored = (boot: string | null, seq: bigint | number | null) => ({ lastState: boot === null ? null : { boot_id: boot }, lastStateSeq: seq });

  it.each<[string, string | null, bigint | number | null, string, number, boolean]>([
    ['first snapshot ever (nothing stored)', null, null, BOOT_A, 0, true],
    ['same boot, newer seq', BOOT_A, 5n, BOOT_A, 6, true],
    ['same boot, a gap in seq', BOOT_A, 5n, BOOT_A, 40, true],
    ['same boot, same seq (a retry)', BOOT_A, 5n, BOOT_A, 5, false],
    ['same boot, older seq', BOOT_A, 5n, BOOT_A, 4, false],
    ['same boot, seq 0 after seq 0', BOOT_A, 0n, BOOT_A, 0, false],
    ['new boot with seq 0 (the agent restarted)', BOOT_A, 900n, BOOT_B, 0, true],
    ['new boot with a seq lower than the old one', BOOT_A, 900n, BOOT_B, 3, true],
    ['new boot with a higher seq', BOOT_A, 5n, BOOT_B, 50, true],
    ['stored boot but no stored seq', BOOT_A, null, BOOT_A, 0, true],
    ['the stored seq as a plain number', BOOT_A, 5, BOOT_A, 6, true],
    ['the stored seq as a plain number, stale', BOOT_A, 5, BOOT_A, 5, false],
  ])('%s', (_name, storedBoot, storedSeq, boot, seq, expected) => {
    expect(isFreshSnapshot(stored(storedBoot, storedSeq), { boot_id: boot, seq })).toBe(expected);
  });

  it('a stored lastState that is not an object (corrupt) counts as no stored boot', () => {
    expect(isFreshSnapshot({ lastState: 'garbage', lastStateSeq: 9n }, { boot_id: BOOT_A, seq: 0 })).toBe(true);
    expect(isFreshSnapshot({ lastState: { boot_id: 5 }, lastStateSeq: 9n }, { boot_id: BOOT_A, seq: 0 })).toBe(true);
  });

  it('handles a seq beyond 2^31 without overflow', () => {
    expect(isFreshSnapshot(stored(BOOT_A, 3_000_000_000n), { boot_id: BOOT_A, seq: 3_000_000_001 })).toBe(true);
    expect(isFreshSnapshot(stored(BOOT_A, 3_000_000_000n), { boot_id: BOOT_A, seq: 3_000_000_000 })).toBe(false);
  });
});

describe('carryOverReady', () => {
  it('keeps the previous ready of a root whose new ready is null', () => {
    const previous = ai3();
    const next = ai3();
    next.seq += 1;
    next.ark.roots[0]!.ready = null;
    const merged = carryOverReady(previous, next);
    expect(merged.ark.roots[0]!.ready).toEqual(previous.ark.roots[0]!.ready);
    expect(merged.ark.roots[0]!.ready).not.toBeNull();
  });

  it('a new non-null ready replaces the previous one', () => {
    const previous = ai3();
    const next = ai3();
    next.ark.roots[0]!.ready!.generated_at = '2026-11-03T10:00:00Z';
    const merged = carryOverReady(previous, next);
    expect(merged.ark.roots[0]!.ready!.generated_at).toBe('2026-11-03T10:00:00Z');
  });

  it('keeps ready per root id, not per position', () => {
    const previous = ai3();
    const next = ai3();
    next.ark.roots = [next.ark.roots[1]!, next.ark.roots[0]!];
    next.ark.roots[1]!.ready = null; // internal, now second
    const merged = carryOverReady(previous, next);
    expect(merged.ark.roots.map((root) => root.id)).toEqual(['ark-0a1b2c3d', 'internal']);
    expect(merged.ark.roots[1]!.ready).toEqual(previous.ark.roots[0]!.ready);
    expect(merged.ark.roots[0]!.ready).toBeNull(); // the drive never had one
  });

  it('stays null when the root had none before, or there is no previous state', () => {
    const next = ai3();
    next.ark.roots[0]!.ready = null;
    expect(carryOverReady(null, next).ark.roots[0]!.ready).toBeNull();
    expect(carryOverReady({ ark: { roots: 'garbage' } }, next).ark.roots[0]!.ready).toBeNull();
    expect(carryOverReady({ ark: { roots: [null, 5, { id: 'internal', ready: null }] } }, next).ark.roots[0]!.ready).toBeNull();
  });

  it('does not carry a root the agent no longer reports, and does not mutate its input', () => {
    const previous = ai3();
    const next = ai3();
    next.ark.roots = [next.ark.roots[1]!];
    const before = JSON.stringify(next);
    const merged = carryOverReady(previous, next);
    expect(merged.ark.roots.map((root) => root.id)).toEqual(['ark-0a1b2c3d']);
    expect(JSON.stringify(next)).toBe(before);
  });

  it('chains: a null after a null still keeps the last real value', () => {
    const first = ai3();
    const second = carryOverReady(first, { ...ai3(), seq: 42, ark: { ...ai3().ark, roots: ai3().ark.roots.map((root) => ({ ...root, ready: null })) } });
    const third = carryOverReady(second, { ...ai3(), seq: 43, ark: { ...ai3().ark, roots: ai3().ark.roots.map((root) => ({ ...root, ready: null })) } });
    expect(third.ark.roots[0]!.ready).toEqual(first.ark.roots[0]!.ready);
  });
});

describe('diffModels', () => {
  const rowsOf = (snapshot: ReturnType<typeof ai3>): ModelRow[] => snapshot.models.map((model) => ({ modelId: model.id, ...toModelFields(model) }));

  it('creates every model on a first snapshot (known-but-absent models too)', () => {
    const snapshot = ai3();
    const diff = diffModels([], snapshot.models);
    expect(diff.create.map((row) => row.modelId)).toEqual(snapshot.models.map((model) => model.id));
    expect(diff.create.find((row) => row.modelId === 'qwen3-vl-8b-q4km')).toMatchObject({ present: false, verified: false, ctx: null });
    expect(diff).toMatchObject({ update: [], remove: [], changed: true });
  });

  it('an identical snapshot changes nothing', () => {
    const snapshot = ai3();
    expect(diffModels(rowsOf(snapshot), snapshot.models)).toEqual({ create: [], update: [], remove: [], changed: false });
  });

  it('deletes a model that is absent from the snapshot', () => {
    const snapshot = ai3();
    const existing = rowsOf(snapshot);
    snapshot.models = snapshot.models.filter((model) => model.id !== 'qwen3.5-4b-q4km');
    expect(diffModels(existing, snapshot.models)).toMatchObject({ create: [], update: [], remove: ['qwen3.5-4b-q4km'], changed: true });
  });

  it.each<[string, (m: ReturnType<typeof ai3>['models'][number]) => void]>([
    ['status', (m) => (m.status = 'failed')],
    ['present', (m) => (m.present = !m.present)],
    ['verified', (m) => (m.verified = !m.verified)],
    ['loadable', (m) => (m.loadable = !m.loadable)],
    ['loadable_reason', (m) => (m.loadable_reason = 'something else')],
    ['default', (m) => (m.default = !m.default)],
    ['slots', (m) => (m.slots += 1)],
    ['ctx', (m) => (m.ctx = 4096)],
    ['roles', (m) => (m.roles = [...m.roles, 'vision'])],
    ['input_modalities', (m) => (m.input_modalities = ['text', 'audio'])],
  ])('updates a model whose %s changed', (_field, mutate) => {
    const snapshot = ai3();
    const existing = rowsOf(snapshot);
    mutate(snapshot.models[0]!);
    const diff = diffModels(existing, snapshot.models);
    expect(diff.update.map((row) => row.modelId)).toEqual([snapshot.models[0]!.id]);
    expect(diff).toMatchObject({ create: [], remove: [], changed: true });
  });

  it('order of roles matters only as a set of stored positions: a reorder is an update', () => {
    const snapshot = ai3();
    const existing = rowsOf(snapshot);
    snapshot.models[0]!.roles = [...snapshot.models[0]!.roles].reverse();
    expect(diffModels(existing, snapshot.models).update).toHaveLength(1);
  });

  it('keeps the first of a duplicated id inside one snapshot', () => {
    const snapshot = ai3();
    const duplicate = { ...snapshot.models[0]!, status: 'failed' as const };
    const diff = diffModels([], [snapshot.models[0]!, duplicate]);
    expect(diff.create).toHaveLength(1);
    expect(diff.create[0]!.status).toBe('loaded');
  });

  it('handles create, update and delete in one snapshot', () => {
    const snapshot = ai3();
    const existing = rowsOf(snapshot).filter((row) => row.modelId !== 'embeddinggemma-300m-q8');
    existing.push({ ...existing[0]!, modelId: 'old-model-gone' });
    snapshot.models[1]!.status = 'loading';
    const diff = diffModels(existing, snapshot.models);
    expect(diff.create.map((row) => row.modelId)).toEqual(['embeddinggemma-300m-q8']);
    expect(diff.update.map((row) => row.modelId)).toEqual([snapshot.models[1]!.id]);
    expect(diff.remove).toEqual(['old-model-gone']);
  });
});

describe('hasModeChanged', () => {
  it.each<[string | null, string, boolean]>([
    [null, 'online', false],
    ['online', 'online', false],
    ['online', 'offline', true],
    ['offline', 'survival', true],
    ['survival', 'online', true],
  ])('%s -> %s is %s', (from, to, expected) => {
    expect(hasModeChanged(from, to)).toBe(expected);
  });
});
