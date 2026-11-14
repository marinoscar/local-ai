// The per-type policy table of docs/specs/cloud-app.md section 3.7: who may send a command, how long a node may hold
// it, how long it may wait in the queue and what makes two requests the same request. Pure data plus tiny helpers,
// so the claim, renew and sweep code, the command API of CLOUD-09 and the tests all read ONE table.
//
// Every number is a wire contract with the Go agent (`agent/internal/cloud/commands.go` `TypeLimits`): the lease is
// what a claim grants, the hard cap is the longest the command may run counted from the claim (the agent's handler
// deadline). A test pins both tables against each other's values.
import { LOCALAI_PERMISSION_IDS, type LocalAiPermissionId } from '../localai.permissions';
import { COMMANDS, type CommandParams, type CommandType } from '../contracts/commands';

const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;

/** What the UI asks before it sends the command (spec 3.7, last column). Data for CLOUD-09 and the web app. */
export type CommandConfirmation =
  | 'none'
  | 'show_planned_size'
  | 'type_model_id'
  | 'offer_dry_run'
  | 'warn_offline_needs_local_action'
  | 'list_clients_to_update'
  | 'show_version_change';

export interface CommandPolicy {
  /**
   * The permission the sender needs: `lab:operate`, `lab:admin` for the destructive types, and the E11 permissions for
   * the two E11 types (`localai_workloads:write`, `localai_music:use`).
   */
  readonly permission: LocalAiPermissionId;
  /** The lease a claim grants and each renewal extends to, from now. */
  readonly leaseMs: number;
  /** How often the agent is told to renew (`renewIntervalMs`); the agent clamps it to 5 to 60 seconds. */
  readonly renewIntervalMs: number;
  /** The longest the command may run, counted from `claimed_at`. A renewal past it is refused. */
  readonly hardCapMs: number;
  /** How long a command may wait `queued` before the sweeper expires it. */
  readonly queuedTtlMs: number;
  /** Claims allowed per command. Always 1: a command runs at most once (spec 1, "at most once"). */
  readonly maxAttempts: 1;
  /** The subject of the dedup key, or null when one active command per node is the rule. */
  readonly dedupSubject: ((params: never) => string) | null;
  readonly confirmation: CommandConfirmation;
}

const OPERATE = LOCALAI_PERMISSION_IDS.LAB_OPERATE;
const ADMIN = LOCALAI_PERMISSION_IDS.LAB_ADMIN;
const WORKLOADS = LOCALAI_PERMISSION_IDS.WORKLOADS_WRITE;
const MUSIC = LOCALAI_PERMISSION_IDS.MUSIC_USE;

/** The subject extractor for a type whose params carry one; typed against that type's own params. */
function subjectOf<T extends CommandType>(pick: (params: CommandParams<T>) => string): (params: never) => string {
  return pick as (params: never) => string;
}

export const COMMAND_POLICY = {
  'localai.status.refresh': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 2 * MINUTE, maxAttempts: 1, dedupSubject: null, confirmation: 'none' },
  'localai.model.load': { permission: OPERATE, leaseMs: 60 * SECOND, renewIntervalMs: 20 * SECOND, hardCapMs: 600 * SECOND, queuedTtlMs: 15 * MINUTE, maxAttempts: 1, dedupSubject: subjectOf<'localai.model.load'>((p) => p.model_id), confirmation: 'none' },
  'localai.model.unload': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 15 * MINUTE, maxAttempts: 1, dedupSubject: subjectOf<'localai.model.unload'>((p) => p.model_id), confirmation: 'none' },
  'localai.model.set_default': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 15 * MINUTE, maxAttempts: 1, dedupSubject: null, confirmation: 'none' },
  'localai.model.download': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 24 * HOUR, maxAttempts: 1, dedupSubject: subjectOf<'localai.model.download'>((p) => p.model_id), confirmation: 'show_planned_size' },
  'localai.model.delete': { permission: ADMIN, leaseMs: 60 * SECOND, renewIntervalMs: 20 * SECOND, hardCapMs: 600 * SECOND, queuedTtlMs: 15 * MINUTE, maxAttempts: 1, dedupSubject: subjectOf<'localai.model.delete'>((p) => p.model_id), confirmation: 'type_model_id' },
  'localai.ark.sync': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 24 * HOUR, maxAttempts: 1, dedupSubject: null, confirmation: 'offer_dry_run' },
  'localai.ark.verify': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 24 * HOUR, maxAttempts: 1, dedupSubject: null, confirmation: 'none' },
  'localai.mode.offline': { permission: ADMIN, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 10 * MINUTE, maxAttempts: 1, dedupSubject: null, confirmation: 'warn_offline_needs_local_action' },
  'localai.key.rotate': { permission: ADMIN, leaseMs: 60 * SECOND, renewIntervalMs: 20 * SECOND, hardCapMs: 600 * SECOND, queuedTtlMs: 10 * MINUTE, maxAttempts: 1, dedupSubject: null, confirmation: 'list_clients_to_update' },
  'localai.service.restart': { permission: OPERATE, leaseMs: 60 * SECOND, renewIntervalMs: 20 * SECOND, hardCapMs: 600 * SECOND, queuedTtlMs: 10 * MINUTE, maxAttempts: 1, dedupSubject: subjectOf<'localai.service.restart'>((p) => p.service), confirmation: 'none' },
  'localai.logs.tail': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 2 * MINUTE, maxAttempts: 1, dedupSubject: subjectOf<'localai.logs.tail'>((p) => p.unit), confirmation: 'none' },
  'localai.catalog.refresh': { permission: OPERATE, leaseMs: 60 * SECOND, renewIntervalMs: 20 * SECOND, hardCapMs: 600 * SECOND, queuedTtlMs: 1 * HOUR, maxAttempts: 1, dedupSubject: null, confirmation: 'none' },
  // "Install what's missing" (ADR-0010): returns at once, like ark.sync; a node that is away for an hour would otherwise
  // start an install nobody remembers asking for.
  'localai.ensure.run': { permission: OPERATE, leaseMs: 30 * SECOND, renewIntervalMs: 10 * SECOND, hardCapMs: 300 * SECOND, queuedTtlMs: 1 * HOUR, maxAttempts: 1, dedupSubject: null, confirmation: 'none' },
  // A 5-minute lease and a 15-minute cap: the agent restarts itself mid-command and the new process renews (spec 5).
  'localai.agent.update': { permission: ADMIN, leaseMs: 300 * SECOND, renewIntervalMs: 60 * SECOND, hardCapMs: 900 * SECOND, queuedTtlMs: 24 * HOUR, maxAttempts: 1, dedupSubject: null, confirmation: 'show_version_change' },
  // E11. A workload switch unloads and loads a set of models with `wait: true`: the model.load numbers. One active
  // switch per node.
  'localai.workload.activate': { permission: WORKLOADS, leaseMs: 60 * SECOND, renewIntervalMs: 20 * SECOND, hardCapMs: 600 * SECOND, queuedTtlMs: 15 * MINUTE, maxAttempts: 1, dedupSubject: null, confirmation: 'none' },
  // E11. Download the sample, generate up to four songs of up to 240 s, upload each: a 30-minute cap. The signed URLs
  // in the params live for the queued TTL plus the hard cap (`music/music-limits.ts`). One command per request.
  'localai.music.generate': { permission: MUSIC, leaseMs: 60 * SECOND, renewIntervalMs: 20 * SECOND, hardCapMs: 1800 * SECOND, queuedTtlMs: 15 * MINUTE, maxAttempts: 1, dedupSubject: subjectOf<'localai.music.generate'>((p) => p.request_id), confirmation: 'none' },
} as const satisfies Record<CommandType, CommandPolicy>;

/** The policy of one command type. */
export function policyOf(type: CommandType): CommandPolicy {
  return COMMAND_POLICY[type];
}

/** The subject placeholder of a type with no subject (one active command per node). */
export const NO_DEDUP_SUBJECT = '-';

/**
 * The dedup key of a command: `"<type>:<subject>"` with the type as the section 3.7 table names it (without the
 * `localai.` prefix: `model.load:qwen3.6-35b-a3b-q4km`), and `-` as the subject of a type that has none. The partial
 * unique index `localai_commands_active_dedup (node_id, dedup_key) WHERE status IN ('queued','running')` makes two
 * active commands with the same key impossible; `params` must already be parsed.
 */
export function dedupKeyFor(type: CommandType, params: unknown): string {
  const subject = COMMAND_POLICY[type].dedupSubject;
  return `${type.slice('localai.'.length)}:${subject === null ? NO_DEDUP_SUBJECT : (subject as (p: unknown) => string)(params)}`;
}

/** Every type the policy table lists (equal to the registry's, proven at compile time below). */
export const POLICY_TYPES = Object.keys(COMMAND_POLICY) as CommandType[];

// A compile-time guard that the registry and the policy table list the same types.
type _SameKeys = Exclude<keyof typeof COMMANDS, keyof typeof COMMAND_POLICY> | Exclude<keyof typeof COMMAND_POLICY, keyof typeof COMMANDS>;
const _sameKeys: [_SameKeys] extends [never] ? true : never = true;
void _sameKeys;

/** Who is asking: the user and the effective permissions of the credential (system grants plus the org role). */
export interface CommandRequester {
  userId: string;
  permissions: readonly string[];
}

/** The permission a caller needs to send (or cancel) a command of `type`: the policy table's, never invented. */
export function permissionFor(type: CommandType): CommandPolicy['permission'] {
  return COMMAND_POLICY[type].permission;
}

/**
 * The types the generic `POST /nodes/:nodeId/commands` route refuses: their params carry signed object-storage URLs
 * that only the cloud may mint, so they are queued by their own feature route (`/api/localai/music/requests`).
 */
export const FEATURE_ONLY_TYPES: ReadonlySet<CommandType> = new Set<CommandType>(['localai.music.generate']);

/**
 * Whether the caller holds the permission the policy table names for `type`. Exact: `lab:admin` does not stand in
 * for `lab:operate` (the table in spec 3.4 grants them separately; the lab owner holds both).
 */
export function maySend(type: CommandType, permissions: readonly string[]): boolean {
  return permissions.includes(permissionFor(type));
}
