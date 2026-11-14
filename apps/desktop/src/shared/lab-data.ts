// Reading what the cloud sends to the Lab (docs/specs/desktop.md 4.8, "As built (DSK-11)"). Pure functions, no Electron:
// the main process uses them before it writes `lab-cache.json`, the Lab page uses them on every answer it gets. Both sides
// rebuild each value field by field from the known names, so nothing the wire carried beyond the contract (and no
// secret-shaped text) survives into the page state or the cache file.
//
// The shape is `NodeCardDto` of `@localai/ui` (cloud-app.md section 3.9); the cloud computes health, mode and ark colours,
// and nothing here recomputes them.
import type {
  ArkColour,
  ArkRoot,
  DrillResult,
  LabHealth,
  LabMode,
  LabPhase,
  LabSummaryDto,
  LoadedModel,
  NodeCardDto,
  SyncResult,
} from '@localai/ui/lab/types';

export const NODE_ID_PATTERN = /^ai-n[1-9][0-9]{0,3}$/;
export const COMMAND_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/** At most this many cards are read or cached; the lab has a handful of computers. */
export const MAX_NODES = 64;
const MAX_ROOTS = 16;
const MAX_MODELS = 16;
const MAX_MODEL_LIST = 64;
const TEXT_LIMIT = 200;
const ID_LIMIT = 64;

// ---------------------------------------------------------------------------------------------------------------------
// Secret-shaped text
// ---------------------------------------------------------------------------------------------------------------------

/** A token prefix of the lab, a bearer header, 64 hex digits (a hash or key) or an address in the tailnet (CGNAT) range. */
const SECRET_SHAPE =
  /\b(?:lat|lai|nod|pat|pg)_[A-Za-z0-9_-]{8,}|\bBearer\s+\S{8,}|\b[0-9a-f]{64}\b|\b100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3}\b/i;

/** Does `text` look like a credential or a tailnet address? The Lab never shows nor stores such text. */
export function secretShaped(text: string): boolean {
  return SECRET_SHAPE.test(text);
}

// ---------------------------------------------------------------------------------------------------------------------
// Small readers
// ---------------------------------------------------------------------------------------------------------------------

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** Text of at most `max` characters, control characters removed; a secret-shaped text becomes null. */
function text(value: unknown, max = TEXT_LIMIT): string | null {
  if (typeof value !== 'string') return null;
  // eslint-disable-next-line no-control-regex
  const clean = value.replace(/[\u0000-\u001f\u007f]/g, ' ').trim();
  if (secretShaped(clean)) return null;
  return clean.length > max ? clean.slice(0, max) : clean;
}

const orNull = <T>(value: T | null | undefined): T | null => (value === undefined ? null : value);
const bool = (value: unknown): boolean => value === true;
const count = (value: unknown): number => (typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.min(Math.floor(value), 1_000_000_000) : 0);
const oneOf = <T extends string>(value: unknown, allowed: readonly T[], fallback: T): T => (typeof value === 'string' && (allowed as readonly string[]).includes(value) ? (value as T) : fallback);
const oneOfOrNull = <T extends string>(value: unknown, allowed: readonly T[]): T | null => (typeof value === 'string' && (allowed as readonly string[]).includes(value) ? (value as T) : null);
const isoOrNull = (value: unknown): string | null => {
  const t = text(value, 40);
  return t !== null && t !== '' && !Number.isNaN(Date.parse(t)) ? t : null;
};

const HEALTH: readonly LabHealth[] = ['healthy', 'degraded', 'down', 'quiet'];
const MODES: readonly LabMode[] = ['online', 'offline', 'survival'];
const PHASES: readonly LabPhase[] = ['pending', 'enrolled', 'retired'];
const COLOURS: readonly ArkColour[] = ['green', 'amber', 'red', 'grey'];
const SYNC_RESULTS: readonly SyncResult[] = ['ok', 'partial', 'failed', 'skipped'];
const DRILL_RESULTS: readonly DrillResult[] = ['pass', 'warn', 'fail'];
const BACKENDS = ['cuda', 'vulkan', 'cpu'] as const;

// ---------------------------------------------------------------------------------------------------------------------
// Node cards
// ---------------------------------------------------------------------------------------------------------------------

function projectRoot(raw: unknown): ArkRoot | null {
  if (!isObject(raw)) return null;
  const rootId = text(raw['rootId'], ID_LIMIT);
  if (rootId === null || rootId === '') return null;
  const age = raw['driveAgeDays'];
  return {
    rootId,
    kind: raw['kind'] === 'ark_drive' ? 'ark_drive' : 'internal',
    present: bool(raw['present']),
    colour: oneOf(raw['colour'], COLOURS, 'grey'),
    label: text(raw['label']) ?? '',
    driveAgeDays: typeof age === 'number' && Number.isFinite(age) && age >= 0 ? Math.floor(age) : null,
  };
}

function projectLoaded(raw: unknown): LoadedModel | null {
  if (!isObject(raw)) return null;
  const id = text(raw['id'], ID_LIMIT);
  return id === null || id === '' ? null : { id, isDefault: bool(raw['isDefault']) };
}

/** One `NodeCardDto`, rebuilt from the known fields. A card without a valid `nodeId` is dropped (null). */
export function projectNodeCard(raw: unknown): NodeCardDto | null {
  if (!isObject(raw)) return null;
  const nodeId = raw['nodeId'];
  if (typeof nodeId !== 'string' || !NODE_ID_PATTERN.test(nodeId)) return null;
  const vram = isObject(raw['vram']) ? raw['vram'] : null;
  const usedMib = vram ? vram['usedMib'] : null;
  const totalMib = vram ? vram['totalMib'] : null;
  const sync = isObject(raw['sync']) ? raw['sync'] : {};
  const drill = isObject(raw['drill']) ? raw['drill'] : {};
  const alerts = isObject(raw['alerts']) ? raw['alerts'] : {};
  const ark = Array.isArray(raw['ark']) ? raw['ark'].slice(0, MAX_ROOTS).map(projectRoot).filter((root): root is ArkRoot => root !== null) : [];
  const loaded = Array.isArray(raw['loadedModels']) ? raw['loadedModels'].slice(0, MAX_MODELS).map(projectLoaded).filter((m): m is LoadedModel => m !== null) : [];
  return {
    nodeId,
    displayName: text(raw['displayName'], 120) || nodeId,
    hardware: orNull(text(raw['hardware'])),
    backend: oneOfOrNull(raw['backend'], BACKENDS),
    phase: oneOf(raw['phase'], PHASES, 'pending'),
    mode: oneOfOrNull(raw['mode'], MODES),
    health: oneOf(raw['health'], HEALTH, 'quiet'),
    routable: bool(raw['routable']),
    lastStateAt: isoOrNull(raw['lastStateAt']),
    loadedModels: loaded,
    lastDefault: orNull(text(raw['lastDefault'], ID_LIMIT)) || null,
    vram:
      typeof usedMib === 'number' && Number.isFinite(usedMib) && usedMib >= 0 && typeof totalMib === 'number' && Number.isFinite(totalMib) && totalMib > 0
        ? { usedMib: Math.floor(usedMib), totalMib: Math.floor(totalMib) }
        : null,
    ark,
    sync: { result: oneOfOrNull(sync['result'], SYNC_RESULTS), finishedAt: isoOrNull(sync['finishedAt']), skipReason: orNull(text(sync['skipReason'])) },
    drill: { result: oneOfOrNull(drill['result'], DRILL_RESULTS) },
    agentVersion: orNull(text(raw['agentVersion'], ID_LIMIT)) || null,
    agentBehind: bool(raw['agentBehind']),
    catalogVersion: orNull(text(raw['catalogVersion'], ID_LIMIT)) || null,
    catalogBehind: bool(raw['catalogBehind']),
    alerts: { active: count(alerts['active']), critical: count(alerts['critical']) },
  };
}

function projectSummary(raw: unknown, nodes: readonly NodeCardDto[]): LabSummaryDto {
  const s = isObject(raw) ? raw : {};
  return {
    nodes: 'nodes' in s ? count(s['nodes']) : nodes.length,
    online: count(s['online']),
    offlineMode: count(s['offlineMode']),
    activeAlerts: count(s['activeAlerts']),
    routable: count(s['routable']),
  };
}

export interface LabNodes {
  summary: LabSummaryDto;
  nodes: NodeCardDto[];
}

/** `{ data: { summary, nodes } }` (the body of `GET /api/localai/nodes`) -> the cards, or null when the body is not that. */
export function projectLabNodes(body: unknown): LabNodes | null {
  const data = isObject(body) && isObject(body['data']) ? body['data'] : null;
  if (!data || !Array.isArray(data['nodes'])) return null;
  const nodes = data['nodes'].slice(0, MAX_NODES).map(projectNodeCard).filter((card): card is NodeCardDto => card !== null);
  return { summary: projectSummary(data['summary'], nodes), nodes };
}

// ---------------------------------------------------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------------------------------------------------

export type CommandStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'expired' | 'cancelled';
const COMMAND_STATUSES: readonly CommandStatus[] = ['queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled'];

export const isSettled = (status: CommandStatus): boolean => status !== 'queued' && status !== 'running';

/** The part of a command the Lab shows. */
export interface CommandInfo {
  id: string;
  nodeId: string;
  type: string;
  status: CommandStatus;
  /** The doctor code of a failure (`LAI-CAT-005`), when the cloud gave one. */
  errorCode: string | null;
  /** The cloud's plain-language message of a failure. */
  errorMessage: string | null;
}

const DOCTOR_CODE = /^LAI-[A-Z]{2,3}-\d{3}$/;

/** `{ data: CommandView }` -> `CommandInfo`, or null. */
export function projectCommand(body: unknown): CommandInfo | null {
  const data = isObject(body) && isObject(body['data']) ? body['data'] : null;
  if (!data) return null;
  const id = data['id'];
  const nodeId = data['nodeId'];
  const status = data['status'];
  if (typeof id !== 'string' || !COMMAND_ID_PATTERN.test(id) || typeof nodeId !== 'string' || !NODE_ID_PATTERN.test(nodeId)) return null;
  if (typeof status !== 'string' || !(COMMAND_STATUSES as readonly string[]).includes(status)) return null;
  const error = isObject(data['error']) ? data['error'] : null;
  const code = error ? text(error['code'], 16) : null;
  return {
    id,
    nodeId,
    type: text(data['type'], ID_LIMIT) ?? '',
    status: status as CommandStatus,
    errorCode: code !== null && DOCTOR_CODE.test(code) ? code : null,
    errorMessage: error ? text(error['message'], 300) || null : null,
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// The models of one node (for Load / Unload / Set default)
// ---------------------------------------------------------------------------------------------------------------------

export interface ModelInfo {
  id: string;
  status: 'loaded' | 'loading' | 'unloaded' | 'failed';
  isDefault: boolean;
  /** The node has the files and the agent says it can load them. */
  loadable: boolean;
}

const MODEL_ID = /^[a-z0-9][a-z0-9.-]{2,63}$/;

/** `{ data: { lastState: { models: [...] } } }` (`GET /api/localai/nodes/:nodeId`) -> the models, or null when the body is not that. */
export function projectModels(body: unknown): ModelInfo[] | null {
  const data = isObject(body) && isObject(body['data']) ? body['data'] : null;
  const state = data && isObject(data['lastState']) ? data['lastState'] : null;
  if (!state) return data ? [] : null;
  const models = state['models'];
  if (!Array.isArray(models)) return [];
  const out: ModelInfo[] = [];
  for (const raw of models.slice(0, MAX_MODEL_LIST)) {
    if (!isObject(raw) || typeof raw['id'] !== 'string' || !MODEL_ID.test(raw['id'])) continue;
    out.push({
      id: raw['id'],
      status: oneOf(raw['status'], ['loaded', 'loading', 'unloaded', 'failed'] as const, 'unloaded'),
      isDefault: bool(raw['default']),
      loadable: bool(raw['present']) && bool(raw['loadable']),
    });
  }
  return out;
}

// ---------------------------------------------------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------------------------------------------------

export interface Refusal {
  /** The doctor code in the body (`LAI-CLD-034`, `LAI-CLD-035`), if any. */
  code: string | null;
  /** `details.reason`, if any (`model_not_on_node`, `node_offline_mode`, ...). */
  reason: string | null;
  /** `details.mode` of LAI-CLD-034: `offline` or `survival`. */
  mode: 'offline' | 'survival' | null;
}

function find(value: unknown, key: string, depth: number): unknown {
  if (depth < 0 || !isObject(value)) return undefined;
  if (key in value) return value[key];
  for (const nested of Object.values(value)) {
    const found = find(nested, key, depth - 1);
    if (found !== undefined) return found;
  }
  return undefined;
}

/** What an error body says, wherever the platform's error envelope puts it (`error.details.code`, `details.code`, `code`). */
export function readRefusal(body: unknown): Refusal {
  const code = find(body, 'code', 4);
  const reason = find(body, 'reason', 4);
  const mode = find(body, 'mode', 4);
  return {
    code: typeof code === 'string' && DOCTOR_CODE.test(code) ? code : null,
    reason: typeof reason === 'string' && /^[a-z_]{1,48}$/.test(reason) ? reason : null,
    mode: mode === 'survival' ? 'survival' : mode === 'offline' ? 'offline' : null,
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// Workloads and music (issue #515, E11)
// ---------------------------------------------------------------------------------------------------------------------

/** A workload's name as profiles and `node.yaml` spell it; the same pattern as the Lab allowlist (cloud-api.ts). */
export const WORKLOAD_NAME_PATTERN = /^[a-z][a-z0-9_-]{0,31}$/;
const MAX_WORKLOADS = 16;

export interface WorkloadInfo {
  name: string;
  label: { en: string; es: string };
  active: boolean;
  /** False when the node said the set does not fit its GPU (LAI-LLM-051). */
  fits: boolean;
  /** Model ids of the set that are not on the node yet. */
  missing: number;
}

/**
 * `{ data: { lastState: { workloads } } }` (`GET /api/localai/nodes/:nodeId`) -> the node's workloads, as the agent's
 * `GET /api/v1/workloads` reports them (`{ workloads: [...], active }`, or the bare list). Null when the body is not a
 * node; an empty list when the node has not reported any (an agent from before E11).
 */
export function projectWorkloads(body: unknown): WorkloadInfo[] | null {
  const data = isObject(body) && isObject(body['data']) ? body['data'] : null;
  if (!data) return null;
  const state = isObject(data['lastState']) ? data['lastState'] : null;
  const raw = state ? state['workloads'] : undefined;
  const block = isObject(raw) ? raw : null;
  const list = Array.isArray(raw) ? raw : block && Array.isArray(block['workloads']) ? block['workloads'] : [];
  const activeName = block && typeof block['active'] === 'string' ? block['active'] : null;
  const out: WorkloadInfo[] = [];
  for (const item of list.slice(0, MAX_WORKLOADS)) {
    if (!isObject(item) || typeof item['name'] !== 'string' || !WORKLOAD_NAME_PATTERN.test(item['name'])) continue;
    const name = item['name'];
    const label = isObject(item['label']) ? item['label'] : {};
    const en = text(label['en'], 40) || name;
    const es = text(label['es'], 40) || en;
    out.push({
      name,
      label: { en, es },
      active: bool(item['active']) || activeName === name,
      fits: item['fits'] !== false,
      missing: Array.isArray(item['missing']) ? Math.min(item['missing'].length, 99) : 0,
    });
  }
  return out;
}

/** Where a music request stands (contract §6: it rides a `localai.music.generate` command, so the command's states). */
export type MusicRequestStatus = CommandStatus;

export interface MusicRequestInfo {
  id: string;
  status: MusicRequestStatus;
  errorCode: string | null;
  errorMessage: string | null;
}

/** Words some APIs use for the end states, read as the command states. */
const MUSIC_STATUS_ALIASES: Readonly<Record<string, MusicRequestStatus>> = { done: 'succeeded', completed: 'succeeded', complete: 'succeeded', error: 'failed', canceled: 'cancelled', pending: 'queued' };

/** `{ data: { id, status, error? } }` (`POST /music/requests`, `GET /music/requests/:id`) -> the request, or null. */
export function projectMusicRequest(body: unknown): MusicRequestInfo | null {
  const data = isObject(body) && isObject(body['data']) ? body['data'] : null;
  if (!data) return null;
  const id = data['id'];
  if (typeof id !== 'string' || !COMMAND_ID_PATTERN.test(id)) return null;
  const rawStatus = typeof data['status'] === 'string' ? data['status'] : '';
  const status = (COMMAND_STATUSES as readonly string[]).includes(rawStatus) ? (rawStatus as MusicRequestStatus) : MUSIC_STATUS_ALIASES[rawStatus];
  if (status === undefined) return null;
  const error = isObject(data['error']) ? data['error'] : null;
  const code = error ? text(error['code'], 16) : typeof data['errorCode'] === 'string' ? text(data['errorCode'], 16) : null;
  const message = error ? text(error['message'], 300) : typeof data['errorMessage'] === 'string' ? text(data['errorMessage'], 300) : null;
  return { id, status, errorCode: code !== null && DOCTOR_CODE.test(code) ? code : null, errorMessage: message || null };
}

export interface SongInfo {
  id: string;
  nodeId: string | null;
  title: string;
  durationS: number | null;
  createdAt: string | null;
}

const MAX_SONGS = 50;

/** `{ data: [...] }` or `{ data: { items | songs: [...] } }` (`GET /music/songs`) -> the songs, newest first as sent. */
export function projectSongs(body: unknown): SongInfo[] | null {
  if (!isObject(body)) return null;
  const data = body['data'];
  const list = Array.isArray(data) ? data : isObject(data) && Array.isArray(data['items']) ? data['items'] : isObject(data) && Array.isArray(data['songs']) ? data['songs'] : null;
  if (list === null) return null;
  const out: SongInfo[] = [];
  for (const raw of list.slice(0, MAX_SONGS)) {
    if (!isObject(raw) || typeof raw['id'] !== 'string' || !COMMAND_ID_PATTERN.test(raw['id'])) continue;
    const nodeId = typeof raw['nodeId'] === 'string' && NODE_ID_PATTERN.test(raw['nodeId']) ? raw['nodeId'] : null;
    const duration = raw['durationS'] ?? raw['duration_s'];
    const created = raw['createdAt'] ?? raw['created_at'];
    out.push({
      id: raw['id'],
      nodeId,
      title: text(raw['title'], 120) || text(raw['tags'], 120) || '',
      durationS: typeof duration === 'number' && Number.isFinite(duration) && duration >= 0 ? Math.round(duration) : null,
      createdAt: typeof created === 'string' && !Number.isNaN(Date.parse(created)) ? created : null,
    });
  }
  return out;
}
