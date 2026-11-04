// The pure parts of state ingestion (docs/specs/cloud-app.md section 4.3): the ordering decision, the READY carry
// over, and the diff of `models[]` into `localai_node_models`. No I/O, no Nest: table-tested in
// `state.projection.spec.ts`, used by `StateService` inside its transaction.
import type { LocalaiStateSnapshotV1 } from '../contracts/state.schema';

type Snapshot = LocalaiStateSnapshotV1;
type SnapshotModel = Snapshot['models'][number];

/** What the stored row says about the last accepted snapshot. */
export interface StoredState {
  /** `lastState` (verbatim JSON of the last accepted snapshot), or null before the first one. */
  lastState: unknown;
  /** `lastStateSeq`, or null before the first snapshot. */
  lastStateSeq: bigint | number | null;
}

/**
 * Accept when this is a new agent boot (the `boot_id` differs from the stored one: the seq counter restarted), or
 * when `seq` is newer than the stored one within the same boot. A repeated or older `seq` is stale.
 */
export function isFreshSnapshot(stored: StoredState, incoming: Pick<Snapshot, 'boot_id' | 'seq'>): boolean {
  const storedBoot = bootIdOf(stored.lastState);
  if (storedBoot === null || storedBoot !== incoming.boot_id) return true;
  if (stored.lastStateSeq === null || stored.lastStateSeq === undefined) return true;
  return BigInt(incoming.seq) > BigInt(stored.lastStateSeq);
}

function bootIdOf(lastState: unknown): string | null {
  if (lastState === null || typeof lastState !== 'object') return null;
  const boot = (lastState as { boot_id?: unknown }).boot_id;
  return typeof boot === 'string' ? boot : null;
}

/**
 * The agent sends `ready: null` for a root whose READY.json did not change since its last accepted snapshot; the
 * cloud keeps the last non-null value per root id. Returns a copy of `next` with each null `ready` replaced by the
 * previous snapshot's value for the same root id (if it had one). A root that is not in `next` is not carried: the
 * agent no longer reports it.
 */
export function carryOverReady(previous: unknown, next: Snapshot): Snapshot {
  const previousReady = new Map<string, unknown>();
  if (previous !== null && typeof previous === 'object') {
    const roots = (previous as { ark?: { roots?: unknown } }).ark?.roots;
    if (Array.isArray(roots)) {
      for (const root of roots) {
        if (root && typeof root === 'object') {
          const { id, ready } = root as { id?: unknown; ready?: unknown };
          if (typeof id === 'string' && ready !== null && ready !== undefined) previousReady.set(id, ready);
        }
      }
    }
  }
  return {
    ...next,
    ark: {
      ...next.ark,
      roots: next.ark.roots.map((root) =>
        root.ready === null && previousReady.has(root.id) ? { ...root, ready: previousReady.get(root.id) as typeof root.ready } : root,
      ),
    },
  };
}

/** The projected columns of one `localai_node_models` row (everything but the key and `updatedAt`). */
export interface ModelRowFields {
  present: boolean;
  verified: boolean;
  loadable: boolean;
  loadableReason: string | null;
  status: 'loaded' | 'loading' | 'unloaded' | 'failed';
  isDefault: boolean;
  slots: number;
  roles: string[];
  inputModalities: string[];
  ctx: number | null;
}

export interface ModelRow extends ModelRowFields {
  modelId: string;
}

export function toModelFields(model: SnapshotModel): ModelRowFields {
  return {
    present: model.present,
    verified: model.verified,
    loadable: model.loadable,
    loadableReason: model.loadable_reason,
    status: model.status,
    isDefault: model.default,
    slots: model.slots,
    roles: [...model.roles],
    inputModalities: [...model.input_modalities],
    ctx: model.ctx,
  };
}

function sameFields(a: ModelRowFields, b: ModelRowFields): boolean {
  return (
    a.present === b.present &&
    a.verified === b.verified &&
    a.loadable === b.loadable &&
    a.loadableReason === b.loadableReason &&
    a.status === b.status &&
    a.isDefault === b.isDefault &&
    a.slots === b.slots &&
    a.ctx === b.ctx &&
    a.roles.length === b.roles.length &&
    a.roles.every((role, index) => role === b.roles[index]) &&
    a.inputModalities.length === b.inputModalities.length &&
    a.inputModalities.every((modality, index) => modality === b.inputModalities[index])
  );
}

export interface ModelDiff {
  create: ModelRow[];
  update: ModelRow[];
  /** Model ids whose row must be deleted (absent from the snapshot). */
  remove: string[];
  /** True when any row is created, changed or deleted: the gateway routing table must be rebuilt. */
  changed: boolean;
}

/**
 * Diffs the snapshot's `models[]` against the node's stored rows: every snapshot model is upserted (a model that is
 * known but not downloaded is a row with `present = false`), every stored row whose id is absent is deleted. Rows
 * that are identical are not written, so `updated_at` means "last changed".
 */
export function diffModels(existing: ReadonlyArray<ModelRow>, incoming: ReadonlyArray<SnapshotModel>): ModelDiff {
  const stored = new Map(existing.map((row) => [row.modelId, row]));
  const seen = new Set<string>();
  const create: ModelRow[] = [];
  const update: ModelRow[] = [];
  for (const model of incoming) {
    // A duplicated id inside one snapshot keeps its first entry (the agent never sends one; this is defence).
    if (seen.has(model.id)) continue;
    seen.add(model.id);
    const fields = toModelFields(model);
    const current = stored.get(model.id);
    if (!current) create.push({ modelId: model.id, ...fields });
    else if (!sameFields(current, fields)) update.push({ modelId: model.id, ...fields });
  }
  const remove = existing.filter((row) => !seen.has(row.modelId)).map((row) => row.modelId);
  return { create, update, remove, changed: create.length > 0 || update.length > 0 || remove.length > 0 };
}

/** `modeChanged`: the stored mode column is set and differs. The very first snapshot is not a mode change. */
export function hasModeChanged(previousMode: string | null, nextMode: string): boolean {
  return previousMode !== null && previousMode !== nextMode;
}
