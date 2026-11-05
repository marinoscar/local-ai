// Agent client: health, token read, session, status and events (main side only; docs/specs/desktop.md 4.1, agent.md 3.8).
//
// The agent token lives in this object's memory and nowhere else: it is never returned, never passed over IPC, never
// logged, never put in a URL. `request()` adds it as `Authorization: Bearer` and refuses any destination that is not the
// agent's own API, so a caller cannot send it elsewhere. Everything the client needs from the outside (the network, the
// token file, the clock) is injected, so the unit tests run against fake-agent.ts without Electron.
import { existsSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { AGENT_HOST, AGENT_PORT } from './egress-guard';
import { NULL_LOGGER, type Logger } from './log';

export const MODULE = 'agent-client' as const;

export const AGENT_ORIGIN = `http://${AGENT_HOST}:${AGENT_PORT}`;
/** Overview.md 3.2: `root:localai-operators 0640`, content `lat_` + 43 characters of base64url. */
export const TOKEN_PATH = '/etc/localai/agent-token';
export const TOKEN_PATTERN = /^lat_[A-Za-z0-9_-]{43}$/;
/** The `/health.version` major this app works with (desktop.md 5, LAI-DSK-006). */
export const SUPPORTED_API_MAJOR = 0;
/** Environment variable of the test hook: honoured only by `resolveTokenPath` below. */
export const TOKEN_PATH_ENV = 'LOCALAI_DESKTOP_TOKEN_PATH';

/** Written next to the executable by `scripts/packaged-app.mjs test-copy`; a shipped app never has it. */
export const TEST_COPY_MARKER = '.ai-desktop-test-copy';

/** Test hooks (the token path override) work in an unpackaged run and in the e2e test copy only, never in a shipped app. */
export function testHooksAllowed(isPackaged: boolean, execPath: string): boolean {
  return !isPackaged || existsSync(join(dirname(execPath), TEST_COPY_MARKER));
}

/** The token path: `LOCALAI_DESKTOP_TOKEN_PATH` only when the app is not packaged or is the e2e test copy. */
export function resolveTokenPath(env: NodeJS.ProcessEnv, allowOverride: boolean): string {
  const override = env[TOKEN_PATH_ENV];
  return allowOverride && override !== undefined && override.startsWith('/') ? override : TOKEN_PATH;
}

/** `ok` when the major of an agent version string is the supported one. `0.0.0-dev` and `0.1.0` are major 0. */
export function checkApiVersion(version: unknown, supportedMajor: number = SUPPORTED_API_MAJOR): 'ok' | 'mismatch' {
  const match = typeof version === 'string' ? /^v?(\d+)\./.exec(version.trim()) : null;
  return match !== null && Number(match[1]) === supportedMajor ? 'ok' : 'mismatch';
}

// ---------------------------------------------------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------------------------------------------------

export interface AgentHealth {
  ok: boolean;
  version: string;
  mode: string;
  starting: boolean;
}

export type HealthResult = { reachable: true; health: AgentHealth } | { reachable: false; reason: 'timeout' | 'refused' | 'invalid' };

export type TokenFailure = 'permission-denied' | 'missing' | 'empty' | 'invalid' | 'error';
export type TokenResult = { ok: true; changed: boolean } | { ok: false; reason: TokenFailure; errno?: string };

export type SessionResult =
  | { ok: true }
  | { ok: false; kind: 'token'; token: Extract<TokenResult, { ok: false }> }
  | { ok: false; kind: 'unauthorized' | 'unavailable' | 'unreachable' | 'unexpected'; status?: number; code?: string };

/** The parts of `GET /status` the desktop reads. The agent sends more; unknown fields are ignored. */
export interface AgentStatus {
  node?: { id?: string; display_name?: string; agent_version?: string };
  mode?: { value?: string; since?: string | null };
  alerts?: Array<{ code?: string; severity?: string; title?: string; fix?: string }>;
  [field: string]: unknown;
}

export type StatusResult =
  | { ok: true; status: AgentStatus }
  | { ok: false; kind: 'token' | 'unauthorized' | 'unreachable' | 'unexpected'; status?: number };

export interface AgentEvent {
  /** The event id, or null for the synthetic `resync` and `shutdown` frames (they carry no `id:` line). */
  id: number | null;
  type: string;
  at?: string;
  node?: string;
  opId?: string;
  data: unknown;
}

export type ConnectionState = 'connecting' | 'connected' | 'reconnecting' | 'shutdown' | 'unauthorized' | 'closed';

export interface EventsHandlers {
  onEvent(event: AgentEvent): void;
  onState?(state: ConnectionState): void;
  /** "Re-read /status": the agent said `resync`, or the stream came back after a break. */
  onResync?(reason: string): void;
}

export interface EventsOptions {
  /** Reconnect delay: starts at `minMs`, doubles up to `maxMs`, back to `minMs` after a connection that worked. */
  backoff?: { minMs: number; maxMs: number };
  /** The agent pings every 15 s; this long without a byte and the stream is dropped and reopened. */
  stallMs?: number;
}

export interface EventSubscription {
  close(): void;
  readonly state: ConnectionState;
  readonly lastEventId: number | null;
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE';
  /** Serialised as JSON; sets `Content-Type: application/json`. */
  json?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  timeoutMs?: number;
  /**
   * Send no credential at all (no token read, no `Authorization`). For the few routes the agent serves without
   * authentication, `POST /api/v1/mode/survival` above all, which must work even when the token file cannot be read.
   * The destination check of `request()` still applies.
   */
  anonymous?: boolean;
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface AgentClientDeps {
  /** Default `http://127.0.0.1:7070`. Tests point it at a fake agent on another port. */
  baseUrl?: string;
  tokenPath?: string;
  /** Node's `fetch` (health, status, events, `request()`). */
  fetch?: FetchLike;
  /** `session.fromPartition('persist:agent').fetch`: the only call whose cookie must land in the agent partition. */
  sessionFetch?: FetchLike;
  readFile?: (path: string) => Promise<string>;
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>;
  log?: Logger;
}

// ---------------------------------------------------------------------------------------------------------------------
// Server-sent events parser
// ---------------------------------------------------------------------------------------------------------------------

export interface SseFrame {
  id?: string;
  event: string;
  data: string;
}

const MAX_SSE_BUFFER = 1024 * 1024;

/** Incremental `text/event-stream` parser (WHATWG): fields id, event, data; `:` comments; frames end at a blank line. */
export class SseParser {
  #buffer = '';
  #id: string | undefined;
  #event = '';
  #data: string[] = [];
  /** True once any bytes (a comment ping included) arrived. */
  activity = false;

  push(chunk: string): SseFrame[] {
    this.activity = true;
    this.#buffer += chunk;
    if (this.#buffer.length > MAX_SSE_BUFFER) throw new Error('event stream line too long');
    const frames: SseFrame[] = [];
    let start = 0;
    for (;;) {
      const lf = this.#buffer.indexOf('\n', start);
      const cr = this.#buffer.indexOf('\r', start);
      let end = lf === -1 ? cr : cr === -1 ? lf : Math.min(lf, cr);
      if (end === -1) break;
      // A lone CR at the very end may be the first half of CRLF: wait for the next chunk.
      if (this.#buffer[end] === '\r' && end === this.#buffer.length - 1) break;
      const line = this.#buffer.slice(start, end);
      end += this.#buffer[end] === '\r' && this.#buffer[end + 1] === '\n' ? 2 : 1;
      start = end;
      const frame = this.#line(line);
      if (frame) frames.push(frame);
    }
    this.#buffer = this.#buffer.slice(start);
    return frames;
  }

  #line(line: string): SseFrame | null {
    if (line === '') {
      if (this.#data.length === 0 && this.#event === '') {
        this.#id = undefined;
        return null;
      }
      const frame: SseFrame = { event: this.#event === '' ? 'message' : this.#event, data: this.#data.join('\n'), ...(this.#id !== undefined ? { id: this.#id } : {}) };
      this.#id = undefined;
      this.#event = '';
      this.#data = [];
      return frame;
    }
    if (line.startsWith(':')) return null;
    const colon = line.indexOf(':');
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'id') this.#id = value;
    else if (field === 'event') this.#event = value;
    else if (field === 'data') this.#data.push(value);
    return null;
  }
}

/** One SSE frame as an `AgentEvent`, or null when its data is not what the agent documents. */
export function frameToEvent(frame: SseFrame): AgentEvent | null {
  let parsed: unknown;
  try {
    parsed = frame.data === '' ? {} : JSON.parse(frame.data);
  } catch {
    return null;
  }
  if (frame.event === 'resync' || frame.event === 'shutdown') return { id: null, type: frame.event, data: parsed };
  if (typeof parsed !== 'object' || parsed === null) return null;
  const envelope = parsed as { id?: unknown; type?: unknown; at?: unknown; node?: unknown; op_id?: unknown; data?: unknown };
  const id = typeof envelope.id === 'number' ? envelope.id : /^\d+$/.test(frame.id ?? '') ? Number(frame.id) : null;
  return {
    id,
    type: typeof envelope.type === 'string' ? envelope.type : frame.event,
    ...(typeof envelope.at === 'string' ? { at: envelope.at } : {}),
    ...(typeof envelope.node === 'string' ? { node: envelope.node } : {}),
    ...(typeof envelope.op_id === 'string' ? { opId: envelope.op_id } : {}),
    data: envelope.data ?? null,
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// The client
// ---------------------------------------------------------------------------------------------------------------------

const defaultSleep = (ms: number, signal?: AbortSignal): Promise<void> =>
  new Promise((resolve) => {
    if (signal?.aborted) return resolve();
    const timer = setTimeout(done, ms);
    function done(): void {
      clearTimeout(timer);
      signal?.removeEventListener('abort', done);
      resolve();
    }
    signal?.addEventListener('abort', done, { once: true });
  });

function errorCode(error: unknown): string | undefined {
  const e = error as { code?: unknown; cause?: { code?: unknown } } | null;
  const code = e?.cause?.code ?? e?.code;
  return typeof code === 'string' ? code : undefined;
}

/** `{error: {code}}` of an agent error envelope, when the body is one. Never reads a success body. */
async function envelopeCode(response: Response): Promise<string | undefined> {
  try {
    const body = (await response.json()) as { error?: { code?: unknown } };
    return typeof body.error?.code === 'string' ? body.error.code : undefined;
  } catch {
    return undefined;
  }
}

export class AgentClient {
  readonly #base: string;
  readonly #origin: string;
  readonly #tokenPath: string;
  readonly #fetch: FetchLike;
  readonly #sessionFetch: FetchLike;
  readonly #readFile: (path: string) => Promise<string>;
  readonly #sleep: (ms: number, signal?: AbortSignal) => Promise<void>;
  readonly #log: Logger;
  #token: string | null = null;

  constructor(deps: AgentClientDeps = {}) {
    this.#base = (deps.baseUrl ?? AGENT_ORIGIN).replace(/\/+$/, '');
    this.#origin = new URL(this.#base).origin;
    this.#tokenPath = deps.tokenPath ?? TOKEN_PATH;
    this.#fetch = deps.fetch ?? ((input, init) => globalThis.fetch(input, init));
    this.#sessionFetch = deps.sessionFetch ?? this.#fetch;
    this.#readFile = deps.readFile ?? ((path) => readFile(path, 'utf8'));
    this.#sleep = deps.sleep ?? defaultSleep;
    this.#log = deps.log ?? NULL_LOGGER;
  }

  /** `GET /api/v1/health`, 1 s timeout, 3 tries 300 ms apart (desktop.md 4.1). No authentication. */
  async health(options: { tries?: number; timeoutMs?: number; delayMs?: number } = {}): Promise<HealthResult> {
    const tries = options.tries ?? 3;
    let reason: Extract<HealthResult, { reachable: false }>['reason'] = 'refused';
    for (let attempt = 1; attempt <= tries; attempt += 1) {
      try {
        const response = await this.#fetch(`${this.#base}/api/v1/health`, { signal: AbortSignal.timeout(options.timeoutMs ?? 1000) });
        const body = (await response.json()) as Partial<AgentHealth>;
        if (response.ok && typeof body.version === 'string') {
          return { reachable: true, health: { ok: body.ok !== false, version: body.version, mode: typeof body.mode === 'string' ? body.mode : 'unknown', starting: body.starting === true } };
        }
        reason = 'invalid';
      } catch (error) {
        const name = (error as { name?: string }).name;
        reason = name === 'TimeoutError' || name === 'AbortError' ? 'timeout' : errorCode(error) === 'ECONNREFUSED' ? 'refused' : error instanceof SyntaxError ? 'invalid' : 'refused';
      }
      if (attempt < tries) await this.#sleep(options.delayMs ?? 300);
    }
    return { reachable: false, reason };
  }

  /**
   * Reads the token file into memory. The result says why it failed, never what the file holds. `changed` is true when
   * the token differs from the one held before (a rotation).
   */
  async readToken(): Promise<TokenResult> {
    let text: string;
    try {
      text = await this.#readFile(this.#tokenPath);
    } catch (error) {
      const errno = (error as NodeJS.ErrnoException).code;
      this.#token = null;
      const reason: TokenFailure = errno === 'EACCES' || errno === 'EPERM' ? 'permission-denied' : errno === 'ENOENT' || errno === 'ENOTDIR' ? 'missing' : 'error';
      return { ok: false, reason, ...(errno !== undefined ? { errno } : {}) };
    }
    const token = text.trim();
    if (token === '') {
      this.#token = null;
      return { ok: false, reason: 'empty' };
    }
    if (!TOKEN_PATTERN.test(token)) {
      this.#token = null;
      return { ok: false, reason: 'invalid' };
    }
    const changed = this.#token !== null && this.#token !== token;
    this.#token = token;
    return { ok: true, changed };
  }

  hasToken(): boolean {
    return this.#token !== null;
  }

  async #ensureToken(): Promise<Extract<TokenResult, { ok: false }> | null> {
    if (this.#token !== null) return null;
    const result = await this.readToken();
    return result.ok ? null : result;
  }

  #authHeader(): Record<string, string> {
    return this.#token === null ? {} : { Authorization: `Bearer ${this.#token}` };
  }

  /** `POST /api/v1/session` through the agent partition, so the `localai_session` cookie lands there. */
  async createSession(): Promise<SessionResult> {
    const failure = await this.#ensureToken();
    if (failure) return { ok: false, kind: 'token', token: failure };
    let response: Response;
    try {
      response = await this.#sessionFetch(`${this.#base}/api/v1/session`, {
        method: 'POST',
        headers: { ...this.#authHeader(), 'Content-Type': 'application/json' },
        body: '{}',
        credentials: 'include',
        signal: AbortSignal.timeout(5000),
      });
    } catch (error) {
      this.#log.log('warn', 'agent', 'session request failed', { reason: errorCode(error) ?? (error as Error).name });
      return { ok: false, kind: 'unreachable' };
    }
    if (response.status === 204 || response.status === 200) return { ok: true };
    const code = await envelopeCode(response);
    const kind = response.status === 401 ? 'unauthorized' : response.status === 503 ? 'unavailable' : 'unexpected';
    return { ok: false, kind, status: response.status, ...(code !== undefined ? { code } : {}) };
  }

  /** Re-reads the token (it may have been rotated) and opens a new session. */
  async recreateSession(): Promise<SessionResult> {
    const token = await this.readToken();
    if (!token.ok) return { ok: false, kind: 'token', token };
    return this.createSession();
  }

  /**
   * An authenticated request to the agent API. `path` must be `/api/v1/...`; any other destination is refused before
   * the token is attached. On a 401 the token file is re-read once and the request repeated if the token changed.
   * Network errors are thrown; HTTP errors are returned.
   */
  async request(path: string, options: RequestOptions = {}): Promise<Response> {
    const url = this.#apiUrl(path);
    const anonymous = options.anonymous === true;
    const failure = anonymous ? null : await this.#ensureToken();
    if (failure) throw new AgentTokenError(failure.reason);
    const send = (): Promise<Response> => {
      const signals = [AbortSignal.timeout(options.timeoutMs ?? 5000), ...(options.signal ? [options.signal] : [])];
      return this.#fetch(url, {
        method: options.method ?? 'GET',
        headers: {
          ...options.headers,
          ...(options.json !== undefined ? { 'Content-Type': 'application/json' } : {}),
          ...(anonymous ? {} : this.#authHeader()),
        },
        ...(options.json !== undefined ? { body: JSON.stringify(options.json) } : {}),
        signal: AbortSignal.any(signals),
      });
    };
    const first = await send();
    if (anonymous || first.status !== 401) return first;
    const reread = await this.readToken();
    if (!reread.ok || !reread.changed) return first;
    return send();
  }

  #apiUrl(path: string): string {
    let url: URL;
    try {
      url = new URL(path, this.#origin);
    } catch {
      throw new RangeError('request path is not a URL path');
    }
    if (!path.startsWith('/') || url.origin !== this.#origin || !url.pathname.startsWith('/api/v1/')) {
      throw new RangeError('request() only talks to the agent API (/api/v1/...)');
    }
    return url.href;
  }

  /** `GET /api/v1/status` with the bearer token. */
  async status(): Promise<StatusResult> {
    let response: Response;
    try {
      response = await this.request('/api/v1/status');
    } catch (error) {
      if (error instanceof AgentTokenError) return { ok: false, kind: 'token' };
      return { ok: false, kind: 'unreachable' };
    }
    if (response.status === 401) return { ok: false, kind: 'unauthorized', status: 401 };
    if (!response.ok) return { ok: false, kind: 'unexpected', status: response.status };
    try {
      return { ok: true, status: (await response.json()) as AgentStatus };
    } catch {
      return { ok: false, kind: 'unexpected', status: response.status };
    }
  }

  /**
   * `GET /api/v1/events` (SSE) in the main process with the bearer token, independent of any renderer cookie.
   * Reconnects with `Last-Event-ID` and a 1 s to 30 s backoff; `resync` and a reconnect tell the handler to re-read /status.
   */
  events(handlers: EventsHandlers, options: EventsOptions = {}): EventSubscription {
    const minMs = options.backoff?.minMs ?? 1000;
    const maxMs = options.backoff?.maxMs ?? 30_000;
    const stallMs = options.stallMs ?? 45_000;
    const url = this.#apiUrl('/api/v1/events');
    let closed = false;
    let state: ConnectionState = 'connecting';
    let lastId: number | null = null;
    let current: AbortController | null = null;

    const setState = (next: ConnectionState): void => {
      if (state === next) return;
      state = next;
      handlers.onState?.(next);
    };

    const consume = async (body: ReadableStream<Uint8Array>, controller: AbortController): Promise<{ shutdown: boolean }> => {
      const parser = new SseParser();
      const decoder = new TextDecoder();
      const reader = body.getReader();
      let stall: ReturnType<typeof setTimeout> | undefined;
      const arm = (): void => {
        clearTimeout(stall);
        stall = setTimeout(() => controller.abort(), stallMs);
      };
      let shutdown = false;
      arm();
      try {
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          arm();
          for (const frame of parser.push(decoder.decode(value, { stream: true }))) {
            const event = frameToEvent(frame);
            if (!event) {
              this.#log.log('warn', 'agent', 'ignored an event the agent sent in an unexpected shape');
              continue;
            }
            if (event.id !== null) lastId = event.id;
            if (event.type === 'shutdown') {
              shutdown = true;
              setState('shutdown');
            } else if (event.type === 'resync') {
              handlers.onResync?.(typeof (event.data as { reason?: unknown })?.reason === 'string' ? ((event.data as { reason: string }).reason) : 'resync');
            }
            handlers.onEvent(event);
          }
        }
      } finally {
        clearTimeout(stall);
        reader.releaseLock();
      }
      return { shutdown };
    };

    const run = async (): Promise<void> => {
      let delay = minMs;
      let first = true;
      while (!closed) {
        setState(first ? 'connecting' : 'reconnecting');
        const controller = new AbortController();
        current = controller;
        try {
          const failure = await this.#ensureToken();
          if (failure) {
            setState('unauthorized');
          } else {
            const response = await this.#fetch(url, {
              headers: { ...this.#authHeader(), Accept: 'text/event-stream', 'Cache-Control': 'no-store', ...(lastId !== null ? { 'Last-Event-ID': String(lastId) } : {}) },
              signal: controller.signal,
            });
            if (response.status === 401) {
              await response.body?.cancel().catch(() => undefined);
              await this.readToken();
              setState('unauthorized');
            } else if (response.status === 200 && response.body && (response.headers.get('content-type') ?? '').includes('text/event-stream')) {
              setState('connected');
              if (!first) handlers.onResync?.('reconnect');
              first = false;
              delay = minMs;
              await consume(response.body, controller);
            } else {
              await response.body?.cancel().catch(() => undefined);
              this.#log.log('warn', 'agent', 'event stream refused', { status: response.status });
            }
          }
        } catch (error) {
          if (!closed) this.#log.log('debug', 'agent', 'event stream ended', { reason: errorCode(error) ?? (error as Error).name });
        } finally {
          controller.abort();
          current = null;
        }
        if (closed) break;
        if (state !== 'shutdown' && state !== 'unauthorized') setState('reconnecting');
        await this.#sleep(delay);
        delay = Math.min(delay * 2, maxMs);
      }
      setState('closed');
    };
    void run();

    return {
      close: () => {
        closed = true;
        current?.abort();
        setState('closed');
      },
      get state() {
        return state;
      },
      get lastEventId() {
        return lastId;
      },
    };
  }
}

/** Thrown by `request()` when the token file cannot be used. Carries the reason, never the file's content. */
export class AgentTokenError extends Error {
  readonly reason: TokenFailure;
  constructor(reason: TokenFailure) {
    super(`the agent token is not available (${reason})`);
    this.name = 'AgentTokenError';
    this.reason = reason;
  }
}

export function createAgentClient(deps: AgentClientDeps = {}): AgentClient {
  return new AgentClient(deps);
}

// ---------------------------------------------------------------------------------------------------------------------
// Feed: what the tray (DSK-06) and the top bar (DSK-04) subscribe to
// ---------------------------------------------------------------------------------------------------------------------

export type FeedMessage =
  | { kind: 'event'; event: AgentEvent }
  | { kind: 'connection'; state: ConnectionState }
  | { kind: 'status'; status: AgentStatus };

/** One event subscription plus the `/status` re-read after `resync` and after a reconnect. */
export class AgentFeed {
  readonly #client: Pick<AgentClient, 'events' | 'status'>;
  readonly #listeners = new Set<(message: FeedMessage) => void>();
  #subscription: EventSubscription | null = null;
  #status: AgentStatus | null = null;
  #connection: ConnectionState = 'closed';

  constructor(client: Pick<AgentClient, 'events' | 'status'>) {
    this.#client = client;
  }

  get status(): AgentStatus | null {
    return this.#status;
  }

  get connection(): ConnectionState {
    return this.#connection;
  }

  subscribe(listener: (message: FeedMessage) => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  /** Seeds the status read at start-up, so a late subscriber has a value at once. */
  seed(status: AgentStatus): void {
    this.#status = status;
  }

  start(options?: EventsOptions): void {
    if (this.#subscription) return;
    this.#subscription = this.#client.events(
      {
        onEvent: (event) => this.#emit({ kind: 'event', event }),
        onState: (state) => {
          this.#connection = state;
          this.#emit({ kind: 'connection', state });
        },
        onResync: () => void this.refresh(),
      },
      options,
    );
  }

  stop(): void {
    this.#subscription?.close();
    this.#subscription = null;
  }

  async refresh(): Promise<void> {
    const result = await this.#client.status();
    if (result.ok) {
      this.#status = result.status;
      this.#emit({ kind: 'status', status: result.status });
    }
  }

  #emit(message: FeedMessage): void {
    for (const listener of this.#listeners) {
      try {
        listener(message);
      } catch {
        // A listener that throws must not stop the others or the stream.
      }
    }
  }
}
