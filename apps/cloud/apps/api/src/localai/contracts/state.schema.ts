// The agent's state snapshot (`POST /api/nodes/{id}/localai/state`, docs/specs/cloud-app.md section 3.6).
//
// This is the contract the Go agent mirrors in `agent/internal/cloud/snapshot_types.go` (agent.md section 4.8.4),
// and the shared JSON fixtures under `test/fixtures/localai/` (`state.ai-n3.json`, `state.lab-3-nodes.json`,
// `state.response.json`) are replayed by both suites. Every object is `.strict()`: a field the cloud does not know
// is a 400, never silently dropped, so a skewed agent is found on its first push.
//
// Field names are a permanent wire contract: a laptop that was offline for months still speaks this schema.
import { z } from 'zod';

export const ModelId = z.string().regex(/^[a-z0-9][a-z0-9.\-]{2,63}$/);
export const ContentId = ModelId;
export const NodeIdStr = z.string().regex(/^ai-n[1-9][0-9]{0,3}$/);
/** ark-sync.md section 2 root id. */
export const RootId = z.string().regex(/^(internal|ark-[0-9a-f]{8})$/);
export const RootKind = z.enum(['internal', 'ark_drive']);
const Iso = z.string().datetime({ offset: true });
const Code = z.string().regex(/^LAI-[A-Z]{2,3}-\d{3}$/);
const Mib = z.number().int().min(0).max(16_777_216);
const Bytes = z.number().int().min(0).max(2 ** 53 - 1);

const ItemStatus = z.enum(['verified', 'stale', 'missing', 'skipped', 'failed', 'quarantined', 'orphan']);

// Full READY.json (ark-sync.md section 4.12.1, authoritative for its fields) minus root.path (drive mount paths
// contain the desktop user name) and minus generated_by.node_id (implicit).
const ReadyItem = z
  .object({
    id: ContentId,
    kind: z.string().max(32),
    tier: z.number().int().min(1).max(3),
    survival: z.boolean().optional(),
    status: ItemStatus,
    version: z.string().max(80).optional(),
    verified_at: Iso.optional(),
    last_reverified_at: Iso.optional(),
    checked_current_at: Iso.optional(),
    age_days: z.number().int().min(0).optional(),
    freshness: z.enum(['green', 'yellow', 'red']).optional(),
    bytes: Bytes.optional(),
    trust: z.enum(['pinned', 'sidecar', 'signed_index', 'computed', 'tofu']).optional(),
    reason: z.string().max(200).optional(),
    code: Code.optional(),
  })
  .strict();

export const ReadyReportV1 = z
  .object({
    schema_version: z.literal(1),
    generated_at: Iso,
    generated_by: z.object({ agent_version: z.string().max(64) }).strict(),
    root: z
      .object({
        id: RootId,
        kind: RootKind,
        profile: z.string().max(64),
        catalog_version: z.string().max(32).nullable(),
        fs_total_bytes: Bytes,
        fs_free_bytes: Bytes,
        budget_bytes: Bytes,
        used_bytes: Bytes,
        trash_bytes: Bytes,
        read_only: z.boolean(),
      })
      .strict(),
    overall: z.enum(['green', 'yellow', 'red']),
    survival_capable: z.boolean(),
    last_sync: z
      .object({
        run_id: z.string().max(64),
        trigger: z.string().max(32),
        finished_at: Iso,
        state: z.string().max(32),
        age_days: z.number().int().min(0),
      })
      .strict()
      .nullable(),
    last_full_reverify_at: Iso.nullable(),
    // Zod 4's `z.record(<enum>, ...)` demands EVERY enum key; a READY.json lists the statuses it has (partial).
    counts: z.partialRecord(ItemStatus, z.number().int().min(0)),
    items: z.array(ReadyItem).max(600),
    problems: z
      .array(
        z
          .object({
            code: Code,
            severity: z.enum(['info', 'warning', 'critical']),
            item: ContentId.optional(),
            title: z.string().max(200),
          })
          .strict(),
      )
      .max(100),
    /** ark-sync.md section 5: the node clock is wrong; items carry no freshness and UIs show age grey. */
    clock_wrong: z.boolean().optional(),
  })
  .strict();
export type ReadyReportV1 = z.infer<typeof ReadyReportV1>;

// Full drill results (drill.md section 3.4, authoritative for its fields); only the fixed test strings appear in details.
export const DrillResultsV1 = z
  .object({
    schema_version: z.literal(1),
    run_id: z.string().max(64),
    trigger: z.string().max(32),
    agent_version: z.string().max(64),
    catalog_version: z.string().max(32).nullable(),
    mode: z.enum(['online', 'offline', 'survival']),
    quick: z.boolean(),
    started_at: Iso,
    finished_at: Iso,
    overall: z.enum(['pass', 'warn', 'fail']),
    survival_ready: z.boolean(),
    counts: z
      .object({
        pass: z.number().int().min(0),
        warn: z.number().int().min(0),
        fail: z.number().int().min(0),
        skip: z.number().int().min(0),
      })
      .strict(),
    checks: z
      .array(
        z
          .object({
            id: z.string().max(96),
            area: z.string().max(32),
            result: z.enum(['pass', 'warn', 'fail', 'skip']),
            code: Code.optional(),
            ms: z.number().int().min(0),
            details: z.record(z.string(), z.union([z.string().max(500), z.number(), z.boolean(), z.null()])).optional(),
          })
          .strict(),
      )
      .max(100),
    restored: z
      .object({ loaded_models: z.array(ModelId).max(16), default_model: ModelId.nullable(), ok: z.boolean() })
      .strict(),
    network_fence: z.object({ enabled: z.boolean(), blocked_dials: z.number().int().min(0) }).strict(),
  })
  .strict();
export type DrillResultsV1 = z.infer<typeof DrillResultsV1>;

const Mode = z.enum(['online', 'offline', 'survival']);

/**
 * "Install what's missing" (ADR-0010): what the node still needs from its own kit and whether the agent is installing it.
 * OPTIONAL on the wire: an agent that predates it sends no `ensure` key and its snapshots still parse (the cloud may run a
 * newer version than a node). A newer agent sends it only to a cloud whose handshake lists `ensure.v1`, because an older
 * cloud's strict schema would refuse the key. No path, no secret: states, counts and step ids and statuses.
 */
export const EnsureBlockV1 = z
  .object({
    state: z.enum(['complete', 'installing', 'needs_you', 'off']),
    auto: z.enum(['on', 'ask', 'off']),
    /** The agent's own sentence in English; the app writes its text in the viewer's language from the codes. */
    message: z.string().max(200),
    progress: z.object({ done: z.number().int().min(0), total: z.number().int().min(0) }).strict(),
    next_action: z
      .object({
        kind: z.enum(['none', 'wait', 'press_button', 'confirm', 'sync', 'fix']),
        step: z.string().max(64).nullable(),
      })
      .strict(),
    steps: z
      .array(
        z
          .object({
            id: z.string().max(64),
            status: z.enum(['ok', 'pending', 'waiting', 'needs_confirm', 'blocked', 'running', 'done', 'failed', 'cancelled']),
          })
          .strict(),
      )
      .max(16),
  })
  .strict();
export type EnsureBlockV1 = z.infer<typeof EnsureBlockV1>;

export const LocalaiStateSnapshotV1 = z
  .object({
    schema_version: z.literal(1),
    node_id: NodeIdStr,
    /** New per agent process start. */
    boot_id: z.string().uuid(),
    /** +1 per snapshot within a boot. */
    seq: z.number().int().min(0),
    reported_at: Iso,
    agent_version: z.string().max(64),
    mode: Mode,
    mode_transition: z.object({ target: Mode, step: z.string().max(64), started_at: Iso }).strict().nullable(),
    connectivity: z.enum(['full', 'internet', 'tailnet', 'none', 'not_checked']),
    profile: z.string().max(64),
    catalog_version: z.string().regex(/^\d{4}\.\d{2}\.\d{2}-\d+$/).nullable(),
    llama: z
      .object({
        /** "<commit>-<backend>", e.g. "f3586e66b-cuda". */
        build: z.string().max(80),
        backend: z.enum(['cuda', 'vulkan', 'cpu']),
        router: z.enum(['up', 'down', 'starting', 'single_mode']),
        models_max: z.number().int().min(1).max(16),
      })
      .strict(),
    hardware: z
      .object({
        gpus: z
          .array(
            z
              .object({
                name: z.string().max(128),
                vram_total_mib: Mib,
                vram_used_mib: Mib,
                temp_c: z.number().min(-50).max(150).nullable(),
              })
              .strict(),
          )
          .max(8),
        ram_total_mib: Mib,
        ram_used_mib: Mib,
        cpu_temp_c: z.number().min(-50).max(150).nullable(),
        power: z
          .object({ source: z.enum(['ac', 'battery', 'unknown']), battery_percent: z.number().min(0).max(100).nullable() })
          .strict(),
      })
      .strict(),
    models: z
      .array(
        z
          .object({
            id: ModelId,
            present: z.boolean(),
            verified: z.boolean(),
            loadable: z.boolean(),
            loadable_reason: z.string().max(200).nullable(),
            status: z.enum(['loaded', 'loading', 'unloaded', 'failed']),
            default: z.boolean(),
            slots: z.number().int().min(1).max(64),
            ctx: z.number().int().min(0).max(4_194_304).nullable(),
            roles: z.array(z.string().max(32)).max(8),
            input_modalities: z.array(z.enum(['text', 'image', 'audio', 'video'])).max(4),
            roots: z.array(RootId).max(9),
          })
          .strict(),
      )
      .max(200),
    services: z
      .array(
        z
          .object({
            name: z.enum(['llama', 'kiwix', 'whisper', 'sync', 'drill']),
            active_state: z.enum(['active', 'inactive', 'failed', 'activating', 'deactivating', 'not_installed']),
            since: Iso.nullable(),
          })
          .strict(),
      )
      .max(8),
    ark: z
      .object({
        roots: z
          .array(
            z
              .object({
                id: RootId,
                /** ark_drive: is a drive mounted now. */
                present: z.boolean(),
                /** READY.json generated_at. */
                ready_generated_at: Iso.nullable(),
                tiers: z
                  .object({
                    t1: z.enum(['ok', 'partial', 'missing']),
                    t2: z.enum(['ok', 'partial', 'missing']),
                    t3: z.enum(['ok', 'partial', 'missing', 'not_planned']),
                  })
                  .strict(),
                items_missing: z.number().int().min(0),
                items_failed: z.number().int().min(0),
                items_skipped_budget: z.number().int().min(0),
                bytes_used: Bytes,
                bytes_free: Bytes,
                last_sync_at: Iso.nullable(),
                last_verify_at: Iso.nullable(),
                /** READY.json overall (ark-sync.md section 4.12.2). */
                overall: z.enum(['green', 'yellow', 'red']),
                /** READY.json survival_capable. */
                survival_capable: z.boolean(),
                /** The full READY.json of this root; null when unchanged since the last accepted snapshot (the cloud keeps the last one). */
                ready: ReadyReportV1.nullable(),
              })
              .strict(),
          )
          .max(9),
        /** Last successful sync TO an AI-ARK drive (survives unplugging). */
        drive_last_synced_at: Iso.nullable(),
      })
      .strict(),
    sync: z
      .object({
        running: z.boolean(),
        run_id: z.string().max(64).nullable(),
        progress_pct: z.number().min(0).max(100).nullable(),
        last_run: z
          .object({
            id: z.string().max(64),
            finished_at: Iso,
            result: z.enum(['ok', 'partial', 'failed', 'skipped']),
            skip_reason: z.string().max(64).nullable(),
            error_code: Code.nullable(),
          })
          .strict()
          .nullable(),
      })
      .strict(),
    drill: z
      .object({
        /** drill.md section 3.4 run_id. */
        run_id: z.string().max(64),
        /** = finished_at of the last run. */
        last_run_at: Iso,
        /** drill.md overall. */
        result: z.enum(['pass', 'warn', 'fail']),
        survival_ready: z.boolean(),
        /** Doctor codes of failed/warned checks. */
        failed_checks: z.array(Code).max(50),
        /** drill.md check ids, e.g. "drl.stt.es". */
        failed_check_ids: z.array(z.string().max(96)).max(50),
        /** The full drill.md section 3.4 document; sent in the first snapshot after a run, then null. */
        results: DrillResultsV1.nullable(),
      })
      .strict()
      .nullable(),
    tailscale: z
      .object({ serve_pattern: z.enum(['A', 'B', 'none']), serve_active: z.boolean(), backend_state: z.string().max(32).nullable() })
      .strict(),
    alerts: z
      .array(z.object({ code: Code, severity: z.enum(['info', 'warning', 'critical']), title: z.string().max(200) }).strict())
      .max(50),
    /** Absent from an agent that predates "Install what's missing" (issue #480). */
    ensure: EnsureBlockV1.optional(),
  })
  .strict();
export type LocalaiStateSnapshotV1 = z.infer<typeof LocalaiStateSnapshotV1>;

/** The body limit of `POST .../state` in bytes (256 KiB; the agent trims its snapshot to fit, agent.md section 4.8.4). */
export const LOCALAI_STATE_BODY_LIMIT_BYTES = 262_144;

/** `200 { data: { accepted: true, seq } }`: the data part (`test/fixtures/localai/state.response.json`). */
export const LocalaiStateResponseSchema = z.object({ accepted: z.literal(true), seq: z.number().int().min(0) }).strict();
export type LocalaiStateResponse = z.infer<typeof LocalaiStateResponseSchema>;
