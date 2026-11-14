// The command registry (docs/specs/cloud-app.md section 3.7): a CLOSED set of 17 command types, each with a strict
// Zod schema for its params (what the cloud may ask) and its result (what the agent may answer). Unknown keys are
// rejected on both sides (`.strict()` here, `DisallowUnknownFields` in the Go agent), so the cloud can queue exactly
// these and nothing else. The Go agent mirrors it in `agent/internal/cloud/jobs`; both load the shared examples in
// `test/fixtures/localai/commands.examples.json`.
//
// A type string is a PERMANENT wire name: it is stored in `localai_commands.type` and sent to every agent.
import { z } from 'zod';

import { ContentId, ModelId, RootId } from './state.schema';

const Iso = z.string().datetime({ offset: true });
const KeyHint = z.string().regex(/^[0-9a-f]{4}…[0-9a-f]{4}$/);
const RunId = z.string().min(1).max(64);
const Semver = z.string().regex(/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/);
/** UTF-8 byte length (the agent's limits are in bytes, not characters). */
const UTF8 = new TextEncoder();
export const utf8Bytes = (value: string): number => UTF8.encode(value).length;

// E11 (docs/specs/music.md, the E11 contract section 6). The limits are the agent's `POST /music/generate` limits.
/** Lyrics: at most 8 KiB of UTF-8. */
export const MUSIC_LYRICS_MAX_BYTES = 8 * 1024;
/** Style tags: at most 512 bytes of UTF-8. */
export const MUSIC_TAGS_MAX_BYTES = 512;
/** A reference sample: at most 20 MiB (and 60 s, which only the agent can measure). */
export const MUSIC_SAMPLE_MAX_BYTES = 20 * 1024 * 1024;
export const MUSIC_DURATION_MIN_S = 10;
export const MUSIC_DURATION_MAX_S = 240;
export const MUSIC_VARIATIONS_MAX = 4;
/** The sample types the agent accepts (wav, mp3, flac, ogg). */
export const MUSIC_SAMPLE_MIMES = ['audio/wav', 'audio/mpeg', 'audio/flac', 'audio/ogg'] as const;
export const MusicSampleMode = z.enum(['sounds_like', 'same_song']);
/** A workload name of a profile or node.yaml (`coding`, `chat`, `speech`, `music`). */
export const WorkloadName = z.string().regex(/^[a-z][a-z0-9-]{1,31}$/);
/** A two-letter language code the music engine takes (`en`, `es`). */
export const MusicLanguage = z.string().regex(/^[a-z]{2}$/);

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
  'localai.ensure.run': {
    // agent API: POST /ensure with the safe default (the automatic steps from the node's own kit, no confirmation, no dry
    // run). Compiling and restarting a running service need a person at the node, so the cloud cannot ask for them: the
    // params are exactly `{}`. Returns at once; the progress arrives in the snapshots' `ensure` block.
    params: z.object({}).strict(),
    result: z
      .object({
        started: z.boolean(), // false = a pass was already running, or there was nothing to do
        state: z.enum(['complete', 'installing', 'needs_you', 'off']),
        pending_steps: z.number().int().min(0).max(16),
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
  'localai.workload.activate': {
    // POST /workloads/{name}/activate with wait: true (agent.md section 3.8.34, llama-router.md section 4.16)
    params: z
      .object({
        name: WorkloadName,
        force: z.boolean().optional(), // the UI never sends true: survival is a decision made at the node
      })
      .strict(),
    result: z.object({ active: z.string() }).strict(),
  },
  'localai.music.generate': {
    // music.md section 4.7: sample pulled by the node, songs pushed to signed PUT URLs. The stored params carry
    // placeholder URLs; the real signed ones are minted at claim time (`music/music-claim.ts`), so no signed URL is
    // ever stored, logged or audited. The command history redacts the lyrics (`command-view.ts`).
    params: z
      .object({
        request_id: z.string().uuid(),
        lyrics: z.string().max(8192), // bytes checked again on the node (UTF-8, <= 8 KiB) and by the cloud's form
        tags: z.string().max(512),
        duration_s: z.number().int().min(10).max(240),
        language: MusicLanguage,
        seed: z.number().int().min(0).max(2147483647).optional(),
        variations: z.number().int().min(1).max(4),
        sample: z
          .object({
            url: z.string().url().startsWith('https://'), // signed GET, short-lived
            sha256: z.string().regex(/^[0-9a-f]{64}$/),
            mime: z.enum(MUSIC_SAMPLE_MIMES),
            size_bytes: z.number().int().min(1).max(20 * 1024 * 1024),
          })
          .strict()
          .optional(),
        sample_mode: MusicSampleMode.optional(), // only with sample
        strength: z.number().min(0).max(1).optional(), // only with sample
        results: z.array(z.object({ put_url: z.string().url().startsWith('https://') }).strict()).min(1).max(4),
      })
      .strict()
      .refine((p) => p.results.length === p.variations, 'results must have one entry per variation')
      .refine((p) => p.sample !== undefined || (p.sample_mode === undefined && p.strength === undefined), 'sample_mode and strength need a sample'),
    result: z
      .object({
        songs: z
          .array(
            z
              .object({
                index: z.number().int().min(0).max(3),
                sha256: z.string().regex(/^[0-9a-f]{64}$/),
                size_bytes: z.number().int().min(1),
                mime: z.string(),
                duration_s: z.number().positive(),
              })
              .strict(),
          )
          .max(4),
      })
      .strict(),
  },
} as const;

export type CommandType = keyof typeof COMMANDS;

/** Every command type, in the order of the registry. */
export const COMMAND_TYPES = Object.keys(COMMANDS) as CommandType[];

/** The prefix the cloud prepends to the names an agent sends (`model.load` -> `localai.model.load`). */
export const COMMAND_TYPE_PREFIX = 'localai.';

/** Whether `value` is one of the 17 command types (with the `localai.` prefix). */
export function isCommandType(value: unknown): value is CommandType {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(COMMANDS, value);
}

/** The params of command type `T` after parsing (defaults applied). */
export type CommandParams<T extends CommandType> = z.output<(typeof COMMANDS)[T]['params']>;
/** The result of command type `T`. */
export type CommandResult<T extends CommandType> = z.output<(typeof COMMANDS)[T]['result']>;
