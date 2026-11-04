// The command registry (docs/specs/cloud-app.md section 3.7): a CLOSED set of 14 command types, each with a strict
// Zod schema for its params (what the cloud may ask) and its result (what the agent may answer). Unknown keys are
// rejected on both sides (`.strict()` here, `DisallowUnknownFields` in the Go agent), so the cloud can queue exactly
// these and nothing else. The Go agent mirrors it in `agent/internal/cloud/jobs`; both load the shared examples in
// `test/fixtures/localai/commands.examples.json`.
//
// A type string is a PERMANENT wire name: it is stored in `localai_commands.type` and sent to every agent.
import { z } from 'zod';

import { ContentId, ModelId, RootId } from './command-ids';

const Iso = z.string().datetime({ offset: true });
const KeyHint = z.string().regex(/^[0-9a-f]{4}…[0-9a-f]{4}$/);
const RunId = z.string().min(1).max(64);
const Semver = z.string().regex(/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/);

export const COMMANDS = {
  'localai.status.refresh': {
    // agent: push a fresh snapshot now (POST …/localai/state), re-report key if needed
    params: z.object({}).strict(),
    result: z.object({ seq: z.number().int().min(0), reported_at: Iso }).strict(),
  },
  'localai.model.load': {
    // agent API: POST /models/{id}/load
    params: z
      .object({
        model_id: ModelId,
        wait: z.literal(true).default(true), // the command always waits for ready (bounded by the lease, section 4.4)
        unload_others: z.boolean().default(true),
        set_default: z.boolean().default(false),
      })
      .strict(),
    result: z
      .object({
        model_id: ModelId,
        status: z.literal('loaded'),
        load_ms: z.number().int().min(0),
        unloaded: z.array(ModelId).max(16),
        vram_used_mib: z.number().int().min(0).nullable(),
        is_default: z.boolean(),
      })
      .strict(),
  },
  'localai.model.unload': {
    // POST /models/{id}/unload
    params: z.object({ model_id: ModelId }).strict(),
    result: z.object({ model_id: ModelId, status: z.literal('unloaded'), was_loaded: z.boolean() }).strict(),
  },
  'localai.model.set_default': {
    // PUT /models/default
    params: z.object({ model_id: ModelId }).strict(),
    result: z.object({ model_id: ModelId, previous: ModelId.nullable() }).strict(),
  },
  'localai.model.download': {
    // POST /models/{id}/download (queues on the sync engine; returns at once)
    params: z.object({ model_id: ModelId, roots: z.array(RootId).min(1).max(9).optional() }).strict(),
    result: z
      .object({
        model_id: ModelId,
        already_present: z.boolean(),
        sync_run_id: RunId.nullable(), // null when already_present
        bytes_planned: z.number().int().min(0),
      })
      .strict(),
  },
  'localai.model.delete': {
    // DELETE /models/{id}  (cloud = admin; the agent applies keep-last, ark-sync.md section 4.10)
    params: z
      .object({
        model_id: ModelId,
        roots: z.array(RootId).min(1).max(9),
        confirm: z.string(), // must equal model_id (typed confirmation in the UI); checked by server AND agent
      })
      .strict()
      .refine((p) => p.confirm === p.model_id, { message: 'confirm must equal model_id', path: ['confirm'] }),
    result: z
      .object({
        model_id: ModelId,
        deleted_roots: z.array(RootId).max(9),
        kept_roots: z
          .array(z.object({ root: RootId, reason: z.enum(['last_verified_copy', 'loaded', 'not_present']) }).strict())
          .max(9),
        freed_bytes: z.number().int().min(0),
      })
      .strict(),
  },
  'localai.ark.sync': {
    // POST /ark/sync (starts; progress arrives in snapshots)
    params: z
      .object({
        roots: z.array(RootId).min(1).max(9).optional(),
        items: z.array(ContentId).min(1).max(200).optional(),
        dry_run: z.boolean().default(false),
      })
      .strict(),
    result: z
      .object({
        sync_run_id: RunId,
        started: z.boolean(), // false = a run was already in progress; sync_run_id is that run
        plan: z
          .object({
            items_to_download: z.number().int().min(0),
            bytes_to_download: z.number().int().min(0),
            items_skipped_budget: z.number().int().min(0),
          })
          .strict(),
      })
      .strict(),
  },
  'localai.ark.verify': {
    // POST /ark/verify (starts)
    params: z
      .object({ roots: z.array(RootId).min(1).max(9).optional(), items: z.array(ContentId).min(1).max(200).optional() })
      .strict(),
    result: z.object({ verify_run_id: RunId, started: z.boolean(), items_planned: z.number().int().min(0) }).strict(),
  },
  'localai.mode.offline': {
    // POST /mode {target: offline}; agent reports the result BEFORE dropping the link
    params: z.object({ reason: z.string().max(200).optional() }).strict(),
    result: z
      .object({ previous_mode: z.enum(['online', 'offline', 'survival']), mode: z.literal('offline') })
      .strict(),
  },
  'localai.key.rotate': {
    // POST /key/rotate; the agent pushes the new key (POST …/key) before reporting this result; `cloud_synced: false`
    // means the push failed and will be retried (LAI-CLD-060)
    params: z.object({}).strict(),
    result: z.object({ fingerprint: KeyHint, rotated_at: Iso, cloud_synced: z.boolean() }).strict(),
  },
  'localai.service.restart': {
    // POST /services/{name}/restart
    params: z.object({ service: z.enum(['llama', 'kiwix', 'whisper']) }).strict(),
    result: z
      .object({
        service: z.enum(['llama', 'kiwix', 'whisper']),
        active_state: z.enum(['active', 'activating', 'failed']),
        restarted_at: Iso,
      })
      .strict(),
  },
  'localai.logs.tail': {
    // GET /logs?unit=&lines=  (secrets scrubbed by the agent; the cloud scrubs again)
    params: z
      .object({
        unit: z.enum(['agent', 'llama', 'kiwix', 'whisper', 'sync', 'drill']), // = localai-<unit>.service
        lines: z.number().int().min(1).max(500).default(200),
      })
      .strict(),
    result: z
      .object({
        unit: z.string().max(64),
        lines: z.array(z.string().max(2000)).max(500),
        truncated: z.boolean(),
      })
      .strict(),
  },
  'localai.catalog.refresh': {
    // POST /catalog/refresh (fetches GET …/localai/catalog)
    params: z.object({}).strict(),
    result: z
      .object({
        catalog_version: z.string().regex(/^\d{4}\.\d{2}\.\d{2}-\d+$/),
        changed: z.boolean(),
        accepted: z.boolean(),
        rejected_code: z.string().regex(/^LAI-[A-Z]{2,3}-\d{3}$/).nullable(),
      })
      .strict(),
  },
  'localai.agent.update': {
    // agent self-update (agent.md section 4.10): GitHub release, sha256 + minisign, rollback
    params: z.object({ version: Semver }).strict(),
    result: z
      .object({
        from_version: Semver,
        to_version: Semver,
        status: z.enum(['updated', 'already_current', 'rolled_back']),
      })
      .strict(),
  },
} as const;

export type CommandType = keyof typeof COMMANDS;

/** Every command type, in the order of the registry. */
export const COMMAND_TYPES = Object.keys(COMMANDS) as CommandType[];

/** The prefix the cloud prepends to the names an agent sends (`model.load` -> `localai.model.load`). */
export const COMMAND_TYPE_PREFIX = 'localai.';

/** Whether `value` is one of the 14 command types (with the `localai.` prefix). */
export function isCommandType(value: unknown): value is CommandType {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(COMMANDS, value);
}

/** The params of command type `T` after parsing (defaults applied). */
export type CommandParams<T extends CommandType> = z.output<(typeof COMMANDS)[T]['params']>;
/** The result of command type `T`. */
export type CommandResult<T extends CommandType> = z.output<(typeof COMMANDS)[T]['result']>;
