// Allowlisted cloud API proxy for the Lab view, and the offline cache behind it (docs/specs/desktop.md 4.8, "As built (DSK-11)").
//
//   decideCloudRequest(raw)   the allowlist: a strict table of (method, path) pairs, read-only plus the seven everyday command
//                             types, a node's workload switch and a music request (issue #515). Pure. Anything else is refused
//                             BEFORE any network call.
//   createCloudProxy(...)     `request()` for the IPC handler: allowlist, 10 s limit, then `CloudSession.request` (which adds the
//                             PAT, registers the call with the abort registry of DSK-10, and refuses unless connected mode is active).
//   createLabCacheStore(...)  `~/.config/ai-desktop/lab-cache.json` (0600): the node cards of the last good load, no secrets.
//
// The page never supplies a URL, a header or a token: it names a method and an allowlisted path, and the main process does the rest.
import { chmod, mkdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { CLOUD_ORIGIN, CloudRequestError } from './cloud-link';
import { NULL_LOGGER, type Logger } from './log';
import { MAX_NODES, NODE_ID_PATTERN, projectLabNodes, secretShaped } from '../shared/lab-data';
import type { CloudFailure, CloudFailureKind, CloudResponse, CloudResult, LabCacheView } from '../shared/types';

export const MODULE = 'cloud-api' as const;

/** The proxy gives up on a request after this long (desktop.md 4.8). */
export const REQUEST_TIMEOUT_MS = 10_000;
export const LAB_CACHE_FILE = 'lab-cache.json';
/** A cache file bigger than this is neither written nor read. */
export const MAX_CACHE_BYTES = 256 * 1024;
const CACHE_SCHEMA = 1;
/** The body of a command is small; a bigger one is not a command. */
const MAX_PARAMS_JSON = 2048;
const MAX_RAW_PATH = 200;

// ---------------------------------------------------------------------------------------------------------------------
// The allowlist
// ---------------------------------------------------------------------------------------------------------------------

/** The seven command types the desktop may send. The cloud still enforces RBAC and validates `params` per type. */
export const COMMAND_TYPES = ['status.refresh', 'model.load', 'model.unload', 'model.set_default', 'ark.sync', 'ark.verify', 'service.restart'] as const;
export type CommandType = (typeof COMMAND_TYPES)[number];

const COMMAND_STATUSES: readonly string[] = ['queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled'];

export type CloudDeny = 'bad_request' | 'method' | 'path' | 'query' | 'command_type' | 'body';

export interface CloudCommandBody {
  type: CommandType;
  params: Record<string, unknown>;
}

/** The body of `POST /api/localai/nodes/:nodeId/workloads/:name/activate` (contract §6): `force` only. */
export interface WorkloadActivateBody {
  force?: boolean;
}

/**
 * The text fields of `POST /api/localai/music/requests`, sent as `multipart/form-data` (contract §6). The Lab sends no
 * sample file: a sample goes through the Music window on this computer or the cloud's own Music page.
 */
export type MusicRequestForm = Readonly<Record<string, string>>;

export type CloudDecision =
  | {
      allow: true;
      method: 'GET' | 'POST';
      /** The path as it will be sent (identical to the raw text). */
      path: string;
      /** A JSON body, rebuilt from what the page sent. */
      body?: CloudCommandBody | WorkloadActivateBody;
      /** Form fields instead of a JSON body (the music request). */
      form?: MusicRequestForm;
    }
  | { allow: false; reason: CloudDeny };

const deny = (reason: CloudDeny): CloudDecision => ({ allow: false, reason });
const isPlainObject = (value: unknown): value is Record<string, unknown> => {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  const proto = Object.getPrototypeOf(value) as unknown;
  return proto === Object.prototype || proto === null;
};

/** The only characters a raw path may hold: letters, digits and `- _ . / ? = &`. No `%`, `\`, `#`, space, control or non-ASCII character. */
const SAFE_RAW = /^\/[A-Za-z0-9\-_./?=&]*$/;
const NODE = 'ai-n[1-9][0-9]{0,3}';
const UUID = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
/** A workload's name as profiles and `node.yaml` spell it (`coding`, `chat`, `speech`, `music`). */
const WORKLOAD = '[a-z][a-z0-9_-]{0,31}';

type QueryRule = ((pairs: ReadonlyMap<string, string>) => boolean) | null;

/** What a route's body must be. Absent: no body at all. */
type BodyRule = 'command' | 'activate' | 'music';

interface Route {
  method: 'GET' | 'POST';
  pattern: RegExp;
  /** `null`: no query string at all. */
  query: QueryRule;
  body?: BodyRule;
}

const alertsQuery: QueryRule = (pairs) => [...pairs.keys()].every((key) => key === 'state') && (pairs.get('state') ?? 'active') === 'active';
/** `?page=N` and nothing else (the music request and song lists). */
const pageQuery: QueryRule = (pairs) => [...pairs].every(([key, value]) => key === 'page' && /^[1-9][0-9]{0,4}$/.test(value));
const commandsQuery: QueryRule = (pairs) => {
  for (const [key, value] of pairs) {
    if (key === 'status') {
      if (!COMMAND_STATUSES.includes(value)) return false;
    } else if (key === 'page') {
      if (!/^[1-9][0-9]{0,4}$/.test(value) || Number(value) > 100_000) return false;
    } else return false;
  }
  return true;
};

const ROUTES: readonly Route[] = [
  { method: 'GET', pattern: /^\/api\/localai\/nodes$/, query: null },
  { method: 'GET', pattern: new RegExp(`^/api/localai/nodes/${NODE}$`), query: null },
  { method: 'GET', pattern: /^\/api\/localai\/alerts$/, query: alertsQuery },
  { method: 'GET', pattern: new RegExp(`^/api/localai/commands/${UUID}$`), query: null },
  { method: 'GET', pattern: new RegExp(`^/api/localai/nodes/${NODE}/commands$`), query: commandsQuery },
  { method: 'POST', pattern: new RegExp(`^/api/localai/nodes/${NODE}/commands$`), query: null, body: 'command' },
  { method: 'GET', pattern: /^\/api\/auth\/me$/, query: null },
  // Issue #515 (E11): switch a node's workload, ask a node for music, and read the requests and songs. A node's workloads
  // are read from the node itself (`GET /api/localai/nodes/:nodeId`, above): the cloud has no separate list route.
  { method: 'POST', pattern: new RegExp(`^/api/localai/nodes/${NODE}/workloads/${WORKLOAD}/activate$`), query: null, body: 'activate' },
  { method: 'POST', pattern: /^\/api\/localai\/music\/requests$/, query: null, body: 'music' },
  { method: 'GET', pattern: /^\/api\/localai\/music\/requests$/, query: pageQuery },
  { method: 'GET', pattern: new RegExp(`^/api/localai/music/requests/${UUID}$`), query: null },
  { method: 'GET', pattern: /^\/api\/localai\/music\/songs$/, query: pageQuery },
  { method: 'GET', pattern: new RegExp(`^/api/localai/music/songs/${UUID}/audio$`), query: null },
];

/** The table as text (`METHOD pattern`), so a test pins exactly which pairs exist. */
export const ROUTE_TABLE: readonly string[] = ROUTES.map((route) => `${route.method} ${route.pattern.source.replace(/\\\//g, '/')}`);

/** `a=1&b=2` -> a map; null for an empty pair, a pair without exactly one `=`, or a repeated key. */
function parseQuery(search: string): Map<string, string> | null {
  const pairs = new Map<string, string>();
  if (search === '') return null;
  for (const part of search.split('&')) {
    const pieces = part.split('=');
    if (pieces.length !== 2 || pieces[0] === '' || pieces[1] === '' || pairs.has(pieces[0]!)) return null;
    pairs.set(pieces[0]!, pieces[1]!);
  }
  return pairs;
}

/** The body of a command POST: `{ type, params? }`, `type` one of the seven, `params` a small plain object. Rebuilt, not copied. */
function commandBody(raw: unknown): CloudCommandBody | CloudDeny {
  if (!isPlainObject(raw)) return 'body';
  if (Object.keys(raw).some((key) => key !== 'type' && key !== 'params')) return 'body';
  const type = raw['type'];
  if (typeof type !== 'string' || !(COMMAND_TYPES as readonly string[]).includes(type)) return 'command_type';
  const params = raw['params'] === undefined ? {} : raw['params'];
  if (!isPlainObject(params)) return 'body';
  let json: string;
  try {
    json = JSON.stringify(params);
  } catch {
    return 'body';
  }
  if (json.length > MAX_PARAMS_JSON) return 'body';
  return { type: type as CommandType, params: JSON.parse(json) as Record<string, unknown> };
}

/** `{ force? }` and nothing else; rebuilt. An absent body is `{}`. */
function activateBody(raw: unknown): WorkloadActivateBody | CloudDeny {
  if (raw === undefined) return {};
  if (!isPlainObject(raw)) return 'body';
  if (Object.keys(raw).some((key) => key !== 'force')) return 'body';
  const force = raw['force'];
  if (force === undefined) return {};
  return typeof force === 'boolean' ? { force } : 'body';
}

/** Byte limits of the music request's text (contract §3: lyrics ≤ 8 KiB, tags ≤ 512 B). */
export const MUSIC_LIMITS = { lyricsBytes: 8 * 1024, tagsBytes: 512, minDuration: 10, maxDuration: 240, minVariations: 1, maxVariations: 4 } as const;
const MUSIC_KEYS = ['nodeId', 'lyrics', 'tags', 'duration_s', 'language', 'seed', 'variations'] as const;
const LANGUAGE = /^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$/;
const isInt = (value: unknown, min: number, max: number): value is number => typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max;
const bytes = (text: string): number => new TextEncoder().encode(text).length;

/**
 * The music request's fields: the shape and the size bounds, so a page cannot make the proxy carry something else. What a
 * good song request is stays the cloud's and the agent's decision. Rebuilt as form fields (all text).
 */
function musicForm(raw: unknown): MusicRequestForm | CloudDeny {
  if (!isPlainObject(raw)) return 'body';
  if (Object.keys(raw).some((key) => !(MUSIC_KEYS as readonly string[]).includes(key))) return 'body';
  const { nodeId, lyrics, tags, duration_s: duration, language, seed, variations } = raw;
  if (typeof nodeId !== 'string' || !NODE_ID_PATTERN.test(nodeId)) return 'body';
  if (typeof lyrics !== 'string' || bytes(lyrics) > MUSIC_LIMITS.lyricsBytes) return 'body';
  if (typeof tags !== 'string' || bytes(tags) > MUSIC_LIMITS.tagsBytes) return 'body';
  if (lyrics.trim() === '' && tags.trim() === '') return 'body';
  if (!isInt(duration, MUSIC_LIMITS.minDuration, MUSIC_LIMITS.maxDuration)) return 'body';
  if (typeof language !== 'string' || !LANGUAGE.test(language)) return 'body';
  if (seed !== undefined && seed !== null && !isInt(seed, 0, 4_294_967_295)) return 'body';
  if (!isInt(variations, MUSIC_LIMITS.minVariations, MUSIC_LIMITS.maxVariations)) return 'body';
  return {
    nodeId,
    lyrics,
    tags,
    duration_s: String(duration),
    language,
    ...(typeof seed === 'number' ? { seed: String(seed) } : {}),
    variations: String(variations),
  };
}

/**
 * The allowlist. `raw` is whatever the page sent. The path is judged on its raw text first (an alphabet of safe characters, no
 * `..` or `//`, no `%`, backslash or fragment), matched against the fixed patterns, and then again after URL normalisation:
 * the normalised path must equal the raw one. An unknown key in the request (`headers`, `url`, ...) refuses it.
 */
export function decideCloudRequest(raw: unknown): CloudDecision {
  if (!isPlainObject(raw)) return deny('bad_request');
  if (Object.keys(raw).some((key) => key !== 'method' && key !== 'path' && key !== 'body')) return deny('bad_request');
  const { method, path, body } = raw;
  if (typeof method !== 'string' || typeof path !== 'string') return deny('bad_request');
  if (path.length === 0 || path.length > MAX_RAW_PATH || !SAFE_RAW.test(path) || path.includes('..') || path.includes('//')) return deny('path');

  const questionMark = path.indexOf('?');
  const pathname = questionMark < 0 ? path : path.slice(0, questionMark);
  const search = questionMark < 0 ? null : path.slice(questionMark + 1);
  if (search !== null && search.includes('?')) return deny('query');

  const matching = ROUTES.filter((route) => route.pattern.test(pathname));
  if (matching.length === 0) return deny('path');
  const route = matching.find((candidate) => candidate.method === method);
  if (!route) return deny('method');

  if (search !== null) {
    const pairs = parseQuery(search);
    if (route.query === null || pairs === null || !route.query(pairs)) return deny('query');
  }

  // The second look: after normalisation the path must be exactly what was matched.
  let normalised: string;
  try {
    const url = new URL(path, CLOUD_ORIGIN);
    if (url.origin !== CLOUD_ORIGIN || url.hash !== '' || url.username !== '' || url.password !== '') return deny('path');
    normalised = `${url.pathname}${url.search}`;
  } catch {
    return deny('path');
  }
  if (normalised !== path) return deny('path');

  if (route.body === 'command') {
    const checked = commandBody(body);
    if (typeof checked === 'string') return deny(checked);
    return { allow: true, method: 'POST', path: normalised, body: checked };
  }
  if (route.body === 'activate') {
    const checked = activateBody(body);
    if (typeof checked === 'string') return deny(checked);
    return { allow: true, method: 'POST', path: normalised, body: checked };
  }
  if (route.body === 'music') {
    const checked = musicForm(body);
    if (typeof checked === 'string') return deny(checked);
    return { allow: true, method: 'POST', path: normalised, form: checked };
  }
  if (body !== undefined) return deny('body');
  return { allow: true, method: 'GET', path: normalised };
}

/** `https://ai.marin.cr/lab/nodes/<nodeId>` for a valid node id, else null. The only address the Lab opens in the browser. */
export function labNodeUrl(nodeId: unknown): string | null {
  return typeof nodeId === 'string' && NODE_ID_PATTERN.test(nodeId) ? `${CLOUD_ORIGIN}/lab/nodes/${nodeId}` : null;
}

/** Is `path` the node list (the one answer that is cached)? */
const isNodeList = (decision: CloudDecision): boolean => decision.allow && decision.method === 'GET' && decision.path === '/api/localai/nodes';

// ---------------------------------------------------------------------------------------------------------------------
// The cache
// ---------------------------------------------------------------------------------------------------------------------

export interface LabCacheStore {
  /** Projects `body` (the answer of `GET /api/localai/nodes`) and writes it. False when nothing was written. */
  save(body: unknown, at?: Date): Promise<boolean>;
  /** The cached cards, or null when there is no usable file. */
  load(): Promise<LabCacheView | null>;
  /** Deletes the file (Disconnect). */
  clear(): Promise<void>;
}

export interface LabCacheOptions {
  /** `~/.config/ai-desktop`. */
  dir: string;
  log?: Logger;
  now?: () => Date;
}

export const labCachePath = (dir: string): string => join(dir, LAB_CACHE_FILE);

/** The text of a cache file for these cards; null when it would be too big or holds anything secret-shaped. */
export function serializeLabCache(body: unknown, savedAt: Date): string | null {
  const projected = projectLabNodes(body);
  if (!projected) return null;
  const view: LabCacheView & { schema: number } = { schema: CACHE_SCHEMA, savedAt: savedAt.toISOString(), summary: projected.summary, nodes: projected.nodes };
  const out = `${JSON.stringify(view, null, 2)}\n`;
  // The projection already drops secret-shaped text; this is the belt over the braces.
  if (secretShaped(out) || Buffer.byteLength(out) > MAX_CACHE_BYTES) return null;
  return out;
}

/** A cache file's text -> the view, or null when it is not a schema-1 cache (every card is projected again). */
export function parseLabCache(content: string): LabCacheView | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(content);
  } catch {
    return null;
  }
  if (!isPlainObject(parsed) || parsed['schema'] !== CACHE_SCHEMA) return null;
  const savedAt = parsed['savedAt'];
  if (typeof savedAt !== 'string' || Number.isNaN(Date.parse(savedAt))) return null;
  const projected = projectLabNodes({ data: { summary: parsed['summary'], nodes: Array.isArray(parsed['nodes']) ? parsed['nodes'].slice(0, MAX_NODES) : null } });
  return projected ? { savedAt, summary: projected.summary, nodes: projected.nodes } : null;
}

export function createLabCacheStore(options: LabCacheOptions): LabCacheStore {
  const file = labCachePath(options.dir);
  const log = options.log ?? NULL_LOGGER;
  const clock = options.now ?? (() => new Date());
  let chain: Promise<unknown> = Promise.resolve();
  let seq = 0;
  // Writes and deletes never interleave.
  const enqueue = <T>(work: () => Promise<T>): Promise<T> => {
    const run = chain.then(work, work);
    chain = run.catch(() => undefined);
    return run;
  };
  return {
    save: (body, at) =>
      enqueue(async () => {
        const content = serializeLabCache(body, at ?? clock());
        if (content === null) {
          log.log('warn', 'cloud', 'the lab cache was not written: the answer was not usable');
          return false;
        }
        seq += 1;
        const tmp = join(options.dir, `.${LAB_CACHE_FILE}.${process.pid}.${seq}.tmp`);
        try {
          await mkdir(options.dir, { recursive: true, mode: 0o700 });
          await writeFile(tmp, content, { mode: 0o600 });
          await chmod(tmp, 0o600);
          await rename(tmp, file);
          return true;
        } catch (error) {
          await rm(tmp, { force: true }).catch(() => undefined);
          log.log('warn', 'cloud', 'the lab cache could not be written', { error: (error as NodeJS.ErrnoException).code ?? 'unknown' });
          return false;
        }
      }),
    load: () =>
      enqueue(async () => {
        try {
          const info = await stat(file);
          if (!info.isFile() || info.size > MAX_CACHE_BYTES) return null;
          return parseLabCache(await readFile(file, 'utf8'));
        } catch {
          return null;
        }
      }),
    clear: () =>
      enqueue(async () => {
        await rm(file, { force: true }).catch(() => undefined);
      }),
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// The proxy
// ---------------------------------------------------------------------------------------------------------------------

/** What the proxy needs from `CloudSession` (cloud-link.ts). */
export interface CloudCaller {
  request(req: { method: 'GET' | 'POST' | 'DELETE'; path: string; body?: unknown; form?: Readonly<Record<string, string>>; signal?: AbortSignal }): Promise<CloudResponse>;
}

export interface TimerPort {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

const realTimers: TimerPort = { set: (fn, ms) => setTimeout(fn, ms), clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>) };

export interface CloudProxyDeps {
  session: CloudCaller;
  cache: LabCacheStore;
  log?: Logger;
  timeoutMs?: number;
  timers?: TimerPort;
}

export interface CloudProxy {
  /** The IPC handler's body: `raw` is whatever the page sent. Never throws. */
  request(raw: unknown): Promise<CloudResult>;
  /** The cached node cards (a local file; no network). */
  cached(): Promise<LabCacheView | null>;
  /** Disconnect: forget the cached cards. */
  clearCache(): Promise<void>;
}

const failure = (error: CloudFailureKind, reason?: string): CloudFailure => ({ ok: false, error, ...(reason !== undefined ? { reason } : {}) });

export function createCloudProxy(deps: CloudProxyDeps): CloudProxy {
  const log = deps.log ?? NULL_LOGGER;
  const timers = deps.timers ?? realTimers;
  const limit = deps.timeoutMs ?? REQUEST_TIMEOUT_MS;
  return {
    async request(raw) {
      const decision = decideCloudRequest(raw);
      if (!decision.allow) {
        // The reason only: never the text the page sent.
        log.log('warn', 'cloud', 'a Lab request was refused by the allowlist', { reason: decision.reason });
        return failure('refused', decision.reason);
      }
      const controller = new AbortController();
      let timedOut = false;
      const handle = timers.set(() => {
        timedOut = true;
        controller.abort();
      }, limit);
      try {
        const response = await deps.session.request({
          method: decision.method,
          path: decision.path,
          ...(decision.body !== undefined ? { body: decision.body } : {}),
          ...(decision.form !== undefined ? { form: decision.form } : {}),
          signal: controller.signal,
        });
        if (response.ok && isNodeList(decision)) await deps.cache.save(response.body).catch(() => false);
        return { ok: response.ok, status: response.status, body: response.body };
      } catch (error) {
        const kind = error instanceof CloudRequestError ? error.kind : 'unreachable';
        const mapped: CloudFailureKind =
          kind === 'aborted' ? (timedOut ? 'timeout' : 'aborted') : kind === 'bad_request' ? 'refused' : kind === 'offline' || kind === 'unauthorized' ? kind : timedOut ? 'timeout' : 'unreachable';
        log.log('info', 'cloud', 'a Lab request got no answer', mapped === 'unreachable' || mapped === 'timeout' ? { kind: mapped, code: 'LAI-DSK-010' } : { kind: mapped });
        return failure(mapped);
      } finally {
        timers.clear(handle);
      }
    },
    cached: () => deps.cache.load(),
    clearCache: () => deps.cache.clear(),
  };
}
