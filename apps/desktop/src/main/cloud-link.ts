// Cloud link: the device flow (RFC 8628) against the lab cloud, the PAT it yields, and connected mode (docs/specs/desktop.md
// sections 4.8 and 4.9, "As built (DSK-10)").
//
// This is the ONLY module that holds the PAT. It never leaves the main process: not to a renderer, not into a log line,
// not into a URL or an argv. `CloudSession` is the typed API the Lab tab (DSK-11) builds on:
//
//   session.connectedModeActive()   linked AND a token is held AND the node's mode is `online` (and not leaving it)
//   session.request(...)            an authenticated call to the cloud, tracked so a mode change can abort it
//   session.onChange(listener)      state changed (link progress, connected on/off)
//
// Everything with a side effect on the world (HTTP, the clock, the settings file, the egress guard, the browser) is
// injected, so the link state machine and the connected-state truth table are unit-tested without Electron or a network.
import net from 'node:net';
import { Agent } from 'undici';
import { CLOUD_HOST, EgressBlockedError, decideMainEgress } from './egress-guard';
import { type Bilingual, type Lang, pick } from './i18n';
import { NULL_LOGGER, type Logger } from './log';
import type { SecretStore, StoredSecret } from './secret-store';
import type { CloudLinkState, CloudResponse, LinkBegin, LinkProgress, LinkRefusal, NodeMode } from '../shared/types';

export const MODULE = 'cloud-link' as const;

export const CLOUD_ORIGIN = `https://${CLOUD_HOST}`;
/** The only page the browser is opened on (desktop.md section 6). */
export const ACTIVATE_URL = `${CLOUD_ORIGIN}/activate`;

export const CODE_NO_KEYRING = 'LAI-DSK-008' as const;
export const CODE_CODE_EXPIRED = 'LAI-DSK-009' as const;
export const CODE_LINK_REMOVED = 'LAI-DSK-011' as const;

/** `slow_down` adds this much to the polling interval (RFC 8628 section 3.5). */
export const SLOW_DOWN_MS = 5000;
/** Consecutive network failures while polling before the attempt is given up (the cloud is not reachable). */
export const MAX_POLL_FAILURES = 5;
/** Aborting cloud requests after a mode change must finish within this long (desktop.md 4.9). */
export const ABORT_BUDGET_MS = 1000;
const MAX_BODY_BYTES = 1024 * 1024;

// ---------------------------------------------------------------------------------------------------------------------
// Texts
// ---------------------------------------------------------------------------------------------------------------------

export const CLOUD_TEXT = {
  switchOnline: { en: 'Switch this computer to Online first.', es: 'Primero cambia esta computadora a modo En línea.' },
  consentTitle: { en: 'No password keyring', es: 'Sin llavero de contraseñas' },
  consentMessage: {
    en: 'This computer has no password keyring. The lab key would be stored with weak protection. Store it anyway?',
    es: 'Esta computadora no tiene un llavero de contraseñas. La clave del laboratorio se guardaría con poca protección. ¿Guardarla de todos modos?',
  },
  consentConfirm: { en: 'Store it anyway', es: 'Guardarla de todos modos' },
  consentCancel: { en: "Don't store it", es: 'No guardarla' },
  removedTitle: { en: 'The lab connection was removed', es: 'Se quitó la conexión con el laboratorio' },
  removedBody: { en: 'Press "Connect to the lab" again.', es: 'Pulsa "Conectar con el laboratorio" otra vez.' },
  linkWindowTitle: { en: 'Connect to the lab', es: 'Conectar con el laboratorio' },
} as const satisfies Record<string, Bilingual>;

export const withCode = (text: string, code: string): string => `${text} (${code})`;
export const textFor = (text: Bilingual, lang: Lang): string => pick(text, lang);

// ---------------------------------------------------------------------------------------------------------------------
// URLs
// ---------------------------------------------------------------------------------------------------------------------

/** `https://ai.marin.cr/activate` or a path below it, default port, no credentials. Nothing else is ever opened in the browser. */
export function isActivationUrl(raw: unknown): raw is string {
  if (typeof raw !== 'string') return false;
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  if (url.protocol !== 'https:' || url.hostname !== CLOUD_HOST || url.port !== '' || url.username !== '' || url.password !== '') return false;
  return url.pathname === '/activate' || url.pathname.startsWith('/activate/');
}

/** A path for the cloud API: absolute, below `/api/`, no traversal, no other origin. Returns the normalised path or null. */
export function cloudApiPath(raw: unknown): string | null {
  if (typeof raw !== 'string' || !raw.startsWith('/api/') || raw.includes('\\')) return null;
  try {
    const url = new URL(raw, CLOUD_ORIGIN);
    if (url.origin !== CLOUD_ORIGIN || !url.pathname.startsWith('/api/') || url.pathname.includes('//')) return null;
    if (url.pathname.split('/').some((segment) => segment === '.' || decodeURIComponent(segment).includes('..'))) return null;
    return `${url.pathname}${url.search}`;
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------------------------------------------------

export interface CloudHttpInit {
  method: 'GET' | 'POST' | 'DELETE';
  body?: unknown;
  /**
   * Text fields sent as `multipart/form-data` instead of a JSON `body` (issue #515: `POST /api/localai/music/requests`).
   * Only the allowlist (cloud-api.ts) builds one; `body` and `form` are never sent together.
   */
  form?: Readonly<Record<string, string>>;
  /** The PAT. It goes into the Authorization header and nowhere else. */
  bearer?: string;
  signal?: AbortSignal;
}

export interface CloudHttpResult {
  status: number;
  /** The parsed JSON body, or null when there is none or it is not JSON. */
  body: unknown;
}

/** One request to the cloud. Rejects when no answer arrived (network error, abort, blocked by the egress guard). */
export type CloudHttp = (path: string, init: CloudHttpInit) => Promise<CloudHttpResult>;

export interface CloudHttpOptions {
  /** Default: `globalThis.fetch` at call time (so the egress guard's wrapper is the one used). */
  fetch?: typeof fetch;
  origin?: string;
  /** Test seam only: an undici dispatcher (see `createLoopbackDispatcher`). */
  dispatcher?: unknown;
}

export function createCloudHttp(options: CloudHttpOptions = {}): CloudHttp {
  const origin = options.origin ?? CLOUD_ORIGIN;
  return async (path, init) => {
    const url = new URL(path, origin);
    if (url.origin !== origin) throw new Error('refusing a request to another origin');
    if (init.body !== undefined && init.form !== undefined) throw new Error('a request has a JSON body or a form, not both');
    const headers: Record<string, string> = { accept: 'application/json' };
    // A form sets its own content type (with the boundary).
    if (init.body !== undefined) headers['content-type'] = 'application/json';
    if (init.bearer !== undefined) headers['authorization'] = `Bearer ${init.bearer}`;
    let form: FormData | undefined;
    if (init.form !== undefined) {
      form = new FormData();
      for (const [name, value] of Object.entries(init.form)) form.append(name, value);
    }
    const doFetch = options.fetch ?? globalThis.fetch;
    const response = await doFetch(url, {
      method: init.method,
      headers,
      ...(init.body !== undefined ? { body: JSON.stringify(init.body) } : {}),
      ...(form !== undefined ? { body: form } : {}),
      // A redirect would carry the bearer somewhere else: the cloud API never redirects.
      redirect: 'error',
      ...(init.signal ? { signal: init.signal } : {}),
      ...(options.dispatcher !== undefined ? { dispatcher: options.dispatcher } : {}),
    } as RequestInit);
    const length = Number(response.headers.get('content-length') ?? '0');
    if (Number.isFinite(length) && length > MAX_BODY_BYTES) throw new Error('the cloud answer is too large');
    const text = await response.text();
    if (text.length > MAX_BODY_BYTES) throw new Error('the cloud answer is too large');
    let body: unknown = null;
    if (text !== '') {
      try {
        body = JSON.parse(text);
      } catch {
        body = null;
      }
    }
    return { status: response.status, body };
  };
}

/**
 * Test seam (e2e only; index.ts offers it in an unpackaged run and in the test copy): connects `ai.marin.cr:443` to a
 * plain-HTTP fake cloud on a loopback port. The URL and the host stay those of the real cloud, and the egress decision is
 * taken on that logical destination (`decide`, the guard's own function) before any socket opens, because the socket that
 * really opens is loopback, which the guard always allows. A closed switch therefore means no socket, exactly as in production.
 */
export function createLoopbackDispatcher(port: number, decide: typeof decideMainEgress = decideMainEgress): Agent {
  return new Agent({
    connect: ((options: { hostname?: string; host?: string; port?: string | number }, callback: (error: Error | null, socket: net.Socket | null) => void) => {
      const decision = decide({ kind: 'tcp', host: options.hostname ?? options.host ?? '', port: options.port === undefined || options.port === '' ? 443 : options.port });
      if (!decision.allow) return callback(new EgressBlockedError(decision.label), null);
      const socket = net.connect({ host: '127.0.0.1', port });
      socket.once('connect', () => callback(null, socket));
      socket.once('error', (error) => callback(error, null));
    }) as never,
  });
}

// ---------------------------------------------------------------------------------------------------------------------
// The device flow
// ---------------------------------------------------------------------------------------------------------------------

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const dataOf = (body: unknown): Record<string, unknown> | null => (isObject(body) && isObject(body['data']) ? body['data'] : null);

export interface DeviceGrant {
  /** Secret: whoever holds it collects the credential. Never shown, never logged. */
  deviceCode: string;
  userCode: string;
  verificationUri: string;
  verificationUriComplete: string;
  expiresIn: number;
  interval: number;
}

export type GrantResult = { ok: true; grant: DeviceGrant } | { ok: false; kind: 'unreachable' | 'unavailable' };

export interface ClientInfo {
  deviceName: string;
  userAgent: string;
}

/** Step 2 of the flow: `POST /api/auth/device/code` asking for a `pat`. */
export async function requestDeviceGrant(http: CloudHttp, info: ClientInfo, signal?: AbortSignal): Promise<GrantResult> {
  let result: CloudHttpResult;
  try {
    result = await http('/api/auth/device/code', {
      method: 'POST',
      body: { clientInfo: { deviceName: info.deviceName, userAgent: info.userAgent, tokenType: 'pat' } },
      ...(signal ? { signal } : {}),
    });
  } catch {
    return { ok: false, kind: 'unreachable' };
  }
  const data = dataOf(result.body);
  if (result.status < 200 || result.status >= 300 || !data) return { ok: false, kind: result.status >= 500 ? 'unreachable' : 'unavailable' };
  const { deviceCode, userCode, verificationUri, verificationUriComplete, expiresIn, interval } = data;
  if (typeof deviceCode !== 'string' || deviceCode === '' || typeof userCode !== 'string' || userCode === '') return { ok: false, kind: 'unavailable' };
  if (typeof expiresIn !== 'number' || !Number.isFinite(expiresIn) || expiresIn <= 0) return { ok: false, kind: 'unavailable' };
  const pollEvery = typeof interval === 'number' && Number.isFinite(interval) ? Math.min(60, Math.max(1, interval)) : 5;
  return {
    ok: true,
    grant: {
      deviceCode,
      userCode,
      verificationUri: typeof verificationUri === 'string' ? verificationUri : ACTIVATE_URL,
      verificationUriComplete: typeof verificationUriComplete === 'string' ? verificationUriComplete : '',
      expiresIn,
      interval: pollEvery,
    },
  };
}

export interface PatCredential {
  token: string;
  tokenId: string;
  expiresAt: string;
}

export type PollOutcome =
  | { kind: 'approved'; credential: PatCredential }
  | { kind: 'denied' }
  | { kind: 'expired' }
  | { kind: 'cancelled' }
  /** The cloud could not be reached `MAX_POLL_FAILURES` times in a row. */
  | { kind: 'unreachable' }
  /** The answer was not what the contract says (another credential kind, a bad token, an unknown error). */
  | { kind: 'failed' };

export interface PollOptions {
  http: CloudHttp;
  deviceCode: string;
  /** Seconds, from the grant. */
  intervalS: number;
  /** Seconds, from the grant. */
  expiresInS: number;
  /** Resolves after `ms`, or early when `signal` aborts (it must not reject). */
  sleep(ms: number, signal: AbortSignal): Promise<void>;
  now(): number;
  signal: AbortSignal;
  maxFailures?: number;
  /** Called with the new interval (ms) after a `slow_down`. */
  onSlowDown?(intervalMs: number): void;
}

const TOKEN_ID = /^[A-Za-z0-9_-]{1,64}$/;

function readCredential(body: unknown): PatCredential | null {
  const data = dataOf(body);
  if (!data) return null;
  // EAB: `credentialType` is the discriminator. A session (JWT + refresh token) is never stored.
  if (data['credentialType'] !== 'pat') return null;
  const { accessToken, tokenId, expiresAt } = data;
  if (typeof accessToken !== 'string' || !accessToken.startsWith('pat_') || accessToken.length < 8) return null;
  if (typeof tokenId !== 'string' || !TOKEN_ID.test(tokenId)) return null;
  if (typeof expiresAt !== 'string' || Number.isNaN(Date.parse(expiresAt))) return null;
  return { token: accessToken, tokenId, expiresAt };
}

/** Steps 4 and 5: poll `POST /api/auth/device/token` until the user decides, the code expires, or the attempt is cancelled. */
export async function pollDeviceToken(options: PollOptions): Promise<PollOutcome> {
  const maxFailures = options.maxFailures ?? MAX_POLL_FAILURES;
  const deadline = options.now() + options.expiresInS * 1000;
  let interval = options.intervalS * 1000;
  let failures = 0;
  for (;;) {
    if (options.signal.aborted) return { kind: 'cancelled' };
    await options.sleep(interval, options.signal);
    if (options.signal.aborted) return { kind: 'cancelled' };
    if (options.now() >= deadline) return { kind: 'expired' };
    let result: CloudHttpResult;
    try {
      result = await options.http('/api/auth/device/token', { method: 'POST', body: { deviceCode: options.deviceCode }, signal: options.signal });
    } catch {
      if (options.signal.aborted) return { kind: 'cancelled' };
      failures += 1;
      if (failures >= maxFailures) return { kind: 'unreachable' };
      continue;
    }
    if (result.status >= 500) {
      failures += 1;
      if (failures >= maxFailures) return { kind: 'unreachable' };
      continue;
    }
    failures = 0;
    if (result.status >= 200 && result.status < 300) {
      const credential = readCredential(result.body);
      return credential ? { kind: 'approved', credential } : { kind: 'failed' };
    }
    const error = isObject(result.body) && typeof result.body['error'] === 'string' ? result.body['error'] : '';
    if (error === 'authorization_pending') continue;
    if (error === 'slow_down' || result.status === 429) {
      interval += SLOW_DOWN_MS;
      options.onSlowDown?.(interval);
      continue;
    }
    if (error === 'expired_token') return { kind: 'expired' };
    if (error === 'access_denied') return { kind: 'denied' };
    return { kind: 'failed' };
  }
}

/** A sleep that ends early when the signal aborts, and never rejects. */
export function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve();
    const done = (): void => {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal.addEventListener('abort', done, { once: true });
  });
}

// ---------------------------------------------------------------------------------------------------------------------
// In-flight cloud requests
// ---------------------------------------------------------------------------------------------------------------------

/** Every cloud request in flight, so one call can cancel them all (desktop.md 4.9: within 1 s of leaving `online`). */
export class AbortRegistry {
  readonly #controllers = new Set<AbortController>();

  /** A controller for one request; remove it when the request settles. */
  create(): AbortController {
    const controller = new AbortController();
    this.#controllers.add(controller);
    return controller;
  }

  release(controller: AbortController): void {
    this.#controllers.delete(controller);
  }

  /** Aborts every request in flight. Synchronous. Returns how many there were. */
  abortAll(): number {
    const all = [...this.#controllers];
    this.#controllers.clear();
    for (const controller of all) controller.abort();
    return all.length;
  }

  get size(): number {
    return this.#controllers.size;
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// The node mode as the session needs it
// ---------------------------------------------------------------------------------------------------------------------

export interface ModeSignal {
  /** The committed mode, or null when it is not known. */
  mode: NodeMode | null;
  /** A transition to `offline`/`survival` has started: the cloud is closed from now on, before the agent finishes. */
  leaving: boolean;
}

const MODE_NAMES: readonly string[] = ['online', 'offline', 'survival'];
const asMode = (value: unknown): NodeMode | null => (typeof value === 'string' && MODE_NAMES.includes(value) ? (value as NodeMode) : null);
const isClosing = (target: unknown): boolean => target === 'offline' || target === 'survival';

/** `GET /status` -> mode and whether a transition to offline/survival is running (`mode.transition.target`, agent.md section 3.5). */
export function modeFromStatus(status: unknown): ModeSignal {
  const mode = isObject(status) && isObject(status['mode']) ? status['mode'] : null;
  const transition = mode && isObject(mode['transition']) ? mode['transition'] : null;
  return { mode: mode ? asMode(mode['value']) : null, leaving: transition !== null && isClosing(transition['target']) };
}

/**
 * One agent event -> the change it makes to the mode signal, or null when it makes none. `mode.step` of a target
 * `offline`/`survival` that has started (or is done) closes the cloud at once; `mode.changed` is the committed mode.
 */
export function modeFromEvent(event: { type: string; data: unknown }): Partial<ModeSignal> | null {
  const data = isObject(event.data) ? event.data : {};
  if (event.type === 'mode.changed') {
    const to = asMode(data['to']);
    return to ? { mode: to, leaving: false } : null;
  }
  if (event.type === 'mode.step' && isClosing(data['target']) && (data['state'] === 'started' || data['state'] === 'done')) return { leaving: true };
  return null;
}

// ---------------------------------------------------------------------------------------------------------------------
// The session
// ---------------------------------------------------------------------------------------------------------------------

export type CloudRequestFailure = 'offline' | 'aborted' | 'unreachable' | 'unauthorized' | 'bad_request';

/** A cloud request that produced no answer. `offline`: refused before any network call (connected mode is not active). */
export class CloudRequestError extends Error {
  readonly kind: CloudRequestFailure;
  constructor(kind: CloudRequestFailure) {
    super(`cloud request failed: ${kind}`);
    this.name = 'CloudRequestError';
    this.kind = kind;
  }
}

export interface CloudSessionDeps {
  http: CloudHttp;
  secrets: SecretStore;
  /** `settings.cloud.linked`. */
  linked(): boolean;
  setLinked(linked: boolean): Promise<void>;
  /** `settings.cloud.url`, for display. */
  url(): string;
  /** Sets the egress guard's `connected` flag (index.ts: `patchEgressState({ connected })`). */
  setEgressConnected(connected: boolean): void;
  hostname(): string;
  version: string;
  /** Opens a URL in the default browser. Only called with an activation URL. */
  openExternal(url: string): void;
  now(): number;
  sleep(ms: number, signal: AbortSignal): Promise<void>;
  /** The link was removed or expired (LAI-DSK-011): tell the user. */
  onRemoved(): void;
  log?: Logger;
}

interface ActiveLink {
  controller: AbortController;
  begun: Promise<LinkBegin | LinkRefusal>;
  grant: DeviceGrant | null;
  deadline: number;
}

const NAME_LIMIT = 80;

/** A display name fit for the UI: no control characters, bounded, and never an email address. */
export function safeDisplayName(raw: unknown): string | undefined {
  if (typeof raw !== 'string') return undefined;
  // eslint-disable-next-line no-control-regex
  const clean = raw.replace(/[\u0000-\u001f\u007f​-‏‪-‮⁦-⁩]/g, '').trim();
  if (clean === '' || clean.includes('@')) return undefined;
  return clean.length > NAME_LIMIT ? `${clean.slice(0, NAME_LIMIT)}...` : clean;
}

export class CloudSession {
  readonly #d: CloudSessionDeps;
  readonly #log: Logger;
  readonly #registry = new AbortRegistry();
  readonly #listeners = new Set<() => void>();
  #secret: StoredSecret | null = null;
  #user: string | undefined;
  #userFetching = false;
  #signal: ModeSignal = { mode: null, leaving: false };
  #link: ActiveLink | null = null;
  #progress: LinkProgress | undefined;
  #egress = false;
  #wasActive = false;

  constructor(deps: CloudSessionDeps) {
    this.#d = deps;
    this.#log = deps.log ?? NULL_LOGGER;
  }

  // -- state -------------------------------------------------------------------------------------------------------

  get tokenHeld(): boolean {
    return this.#secret !== null;
  }

  /** The cloud is open: the node is `online` and not on its way out of it. */
  #modeOpen(): boolean {
    return this.#signal.mode === 'online' && !this.#signal.leaving;
  }

  /** The node is `online` and not on its way out of it (a link attempt may start). */
  get nodeOnline(): boolean {
    return this.#modeOpen();
  }

  /** Desktop.md 4.9: linked AND a token is held AND the node's mode is online. */
  connectedModeActive(): boolean {
    return this.#d.linked() && this.#secret !== null && this.#modeOpen();
  }

  /** Whether `ai.marin.cr:443` is open for the main process: connected mode, or a link attempt on an online node. */
  egressOpen(): boolean {
    return this.#modeOpen() && ((this.#d.linked() && this.#secret !== null) || this.#link !== null);
  }

  state(): CloudLinkState {
    return {
      linked: this.#d.linked(),
      tokenHeld: this.#secret !== null,
      url: this.#d.url(),
      ...(this.#user !== undefined ? { user: this.#user } : {}),
      ...(this.#secret ? { expiresAt: this.#secret.expiresAt } : {}),
      ...(this.#progress ? { link: { ...this.#progress } } : {}),
    };
  }

  onChange(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  /** Re-evaluates the egress switch and tells the listeners. Deactivation aborts every request in flight, synchronously. */
  #changed(): void {
    const open = this.egressOpen();
    if (open !== this.#egress) {
      // The allowlist first, then the aborts: nothing can start in between (everything here is synchronous).
      this.#egress = open;
      this.#d.setEgressConnected(open);
      if (!open) {
        const aborted = this.#registry.abortAll();
        if (aborted > 0) this.#log.log('info', 'cloud', 'cloud requests aborted: connected mode is off', { count: aborted });
      }
    }
    // A link attempt cannot go on once the node leaves `online`.
    if (this.#link && !this.#modeOpen()) this.#link.controller.abort();
    const active = this.connectedModeActive();
    if (active && !this.#wasActive && this.#user === undefined) void this.refreshUser();
    this.#wasActive = active;
    for (const listener of this.#listeners) {
      try {
        listener();
      } catch {
        // a listener must not break the session
      }
    }
  }

  // -- the node's mode ---------------------------------------------------------------------------------------------

  /** Feed the session the agent's mode: from `/status`, `mode.changed` and `mode.step`. */
  setModeSignal(change: Partial<ModeSignal>): void {
    const next: ModeSignal = { mode: change.mode !== undefined ? change.mode : this.#signal.mode, leaving: change.leaving ?? this.#signal.leaving };
    if (next.mode === this.#signal.mode && next.leaving === this.#signal.leaving) return;
    this.#signal = next;
    this.#changed();
  }

  /** The settings (or anything else the state shows) changed. */
  refresh(): void {
    this.#changed();
  }

  // -- requests ----------------------------------------------------------------------------------------------------

  /** One tracked request. `external` is an additional signal (the caller's, or the link attempt's). */
  async #call(path: string, init: Omit<CloudHttpInit, 'signal'>, external?: AbortSignal, timeoutMs?: number): Promise<CloudHttpResult> {
    const controller = this.#registry.create();
    const onExternal = (): void => controller.abort();
    if (external) {
      if (external.aborted) controller.abort();
      else external.addEventListener('abort', onExternal, { once: true });
    }
    const timer = timeoutMs !== undefined ? setTimeout(() => controller.abort(), timeoutMs) : undefined;
    try {
      return await this.#d.http(path, { ...init, signal: controller.signal });
    } finally {
      if (timer) clearTimeout(timer);
      external?.removeEventListener('abort', onExternal);
      this.#registry.release(controller);
    }
  }

  /**
   * An authenticated call to the cloud API (the Lab tab's proxy builds on it; the allowlist of paths is DSK-11's).
   * Refused with `offline` before any network call when connected mode is not active. The PAT is added here and
   * never returned. A `401` deletes the token (LAI-DSK-011). The body is the cloud's JSON as it came (EAB wraps it in `{ data }`).
   */
  async request(req: { method: 'GET' | 'POST' | 'DELETE'; path: string; body?: unknown; form?: Readonly<Record<string, string>>; signal?: AbortSignal }): Promise<CloudResponse> {
    const secret = this.#secret;
    if (!secret || !this.connectedModeActive()) throw new CloudRequestError('offline');
    const path = cloudApiPath(req.path);
    if (path === null) throw new CloudRequestError('bad_request');
    let result: CloudHttpResult;
    try {
      result = await this.#call(path, { method: req.method, ...(req.body !== undefined ? { body: req.body } : {}), ...(req.form !== undefined ? { form: req.form } : {}), bearer: secret.token }, req.signal);
    } catch {
      throw new CloudRequestError(req.signal?.aborted || !this.egressOpen() ? 'aborted' : 'unreachable');
    }
    if (result.status === 401) {
      await this.#forget('removed');
      throw new CloudRequestError('unauthorized');
    }
    return { ok: result.status >= 200 && result.status < 300, status: result.status, body: result.body };
  }

  /** Who is linked: `GET /api/auth/me`, display name only. Best effort; a `401` removes the link. */
  async refreshUser(): Promise<void> {
    if (this.#userFetching || !this.#secret) return;
    this.#userFetching = true;
    try {
      const response = await this.request({ method: 'GET', path: '/api/auth/me' });
      const data = dataOf(response.body);
      const name = response.ok && data ? safeDisplayName(data['displayName']) : undefined;
      if (name !== undefined && name !== this.#user) {
        this.#user = name;
        this.#changed();
      }
    } catch {
      // unreachable now: the next activation tries again
    } finally {
      this.#userFetching = false;
    }
  }

  // -- link --------------------------------------------------------------------------------------------------------

  /**
   * Starts a link attempt (desktop.md 4.8 steps 1 to 4). Refused unless the node is `online`. Calling it while an
   * attempt is running returns that attempt's code with the time left, so two windows show the same code.
   */
  async beginLink(): Promise<LinkBegin | LinkRefusal> {
    if (this.#link) {
      const first = await this.#link.begun;
      return 'userCode' in first ? { ...first, expiresIn: Math.max(1, Math.ceil((this.#link.deadline - this.#d.now()) / 1000)) } : first;
    }
    if (this.#d.linked() && this.#secret) return { ok: false, code: 'already_linked' };
    if (!this.#modeOpen()) return { ok: false, code: 'not_online' };

    const controller = new AbortController();
    const link: ActiveLink = { controller, grant: null, deadline: 0, begun: Promise.resolve({ ok: false, code: 'unavailable' }) };
    this.#link = link;
    this.#progress = { phase: 'pending' };
    // Open the cloud to the main process BEFORE the first request: the request starts synchronously inside #startLink.
    this.#changed();
    link.begun = this.#startLink(link);
    return link.begun;
  }

  async #startLink(link: ActiveLink): Promise<LinkBegin | LinkRefusal> {
    const info: ClientInfo = { deviceName: `ai-desktop on ${this.#d.hostname()}`, userAgent: `ai-desktop/${this.#d.version}` };
    const granted = await requestDeviceGrant((path, init) => this.#call(path, init, link.controller.signal), info, link.controller.signal);
    const aborted = link.controller.signal.aborted;
    if (aborted || !granted.ok) {
      this.#endLink(link, aborted ? { phase: 'cancelled' } : { phase: granted.ok || granted.kind !== 'unreachable' ? 'failed' : 'unreachable' });
      return { ok: false, code: aborted ? 'not_online' : granted.ok ? 'unavailable' : granted.kind };
    }
    link.grant = granted.grant;
    link.deadline = this.#d.now() + granted.grant.expiresIn * 1000;
    void this.#poll(link, granted.grant);
    return { userCode: granted.grant.userCode, verificationUri: ACTIVATE_URL, expiresIn: granted.grant.expiresIn };
  }

  async #poll(link: ActiveLink, grant: DeviceGrant): Promise<void> {
    const outcome = await pollDeviceToken({
      http: (path, init) => this.#call(path, init, link.controller.signal),
      deviceCode: grant.deviceCode,
      intervalS: grant.interval,
      expiresInS: grant.expiresIn,
      sleep: this.#d.sleep,
      now: this.#d.now,
      signal: link.controller.signal,
    });
    switch (outcome.kind) {
      case 'approved':
        await this.#finishLink(link, outcome.credential);
        return;
      case 'denied':
        this.#log.log('warn', 'cloud', 'the lab connection code was refused', { code: CODE_CODE_EXPIRED });
        this.#endLink(link, { phase: 'denied', code: CODE_CODE_EXPIRED });
        return;
      case 'expired':
        this.#log.log('warn', 'cloud', 'the lab connection code expired', { code: CODE_CODE_EXPIRED });
        this.#endLink(link, { phase: 'expired', code: CODE_CODE_EXPIRED });
        return;
      case 'cancelled':
        this.#endLink(link, { phase: 'cancelled' });
        return;
      case 'unreachable':
        this.#endLink(link, { phase: 'unreachable' });
        return;
      default:
        this.#endLink(link, { phase: 'failed' });
    }
  }

  async #finishLink(link: ActiveLink, credential: PatCredential): Promise<void> {
    this.#progress = { phase: 'saving' };
    this.#changed();
    const secret: StoredSecret = { token: credential.token, tokenId: credential.tokenId, expiresAt: credential.expiresAt };
    const saved = await this.#d.secrets.save(secret);
    this.#secret = secret;
    await this.#d.setLinked(true);
    const progress: LinkProgress =
      saved.where === 'memory' ? { phase: 'done', memoryOnly: true, ...(saved.reason === 'no-keyring' ? { code: CODE_NO_KEYRING } : {}) } : { phase: 'done' };
    this.#log.log('info', 'cloud', 'linked to the lab', { storage: saved.where });
    this.#endLink(link, progress);
  }

  #endLink(link: ActiveLink, progress: LinkProgress): void {
    if (this.#link === link) this.#link = null;
    this.#progress = progress;
    this.#changed();
  }

  /** The user closed the link window: stop polling, forget the attempt (a finished one too, so the next window starts clean). */
  cancelLink(): void {
    if (this.#link) {
      this.#link.controller.abort();
      return;
    }
    if (this.#progress && this.#progress.phase !== 'saving') {
      this.#progress = undefined;
      this.#changed();
    }
  }

  /** "Open the browser": the activation URL with the code filled in, only if it is an activation URL, else the plain activation page. */
  openVerification(): boolean {
    const grant = this.#link?.grant;
    if (!grant) return false;
    const target = isActivationUrl(grant.verificationUriComplete) ? grant.verificationUriComplete : ACTIVATE_URL;
    this.#d.openExternal(target);
    return true;
  }

  // -- start-up, unlink --------------------------------------------------------------------------------------------

  /**
   * Start-up (desktop.md 4.8): read the token back. A token that cannot be decrypted (the keyring changed), a link whose
   * token file is gone and a PAT past its expiry all mean "unlinked" and LAI-DSK-011.
   */
  async start(): Promise<void> {
    const loaded = await this.#d.secrets.load();
    if (loaded.kind === 'ok') {
      if (Date.parse(loaded.secret.expiresAt) <= this.#d.now()) {
        await this.#forget('removed');
        return;
      }
      this.#secret = loaded.secret;
      if (!this.#d.linked()) await this.#d.setLinked(true);
      this.#changed();
      return;
    }
    if (loaded.kind === 'unreadable') {
      await this.#forget('removed');
      return;
    }
    if (this.#d.linked()) await this.#forget('removed');
    else this.#changed();
  }

  /** Deletes the token and the link without asking the cloud (it said `401`, or the token is unusable). */
  async #forget(reason: 'removed' | 'user'): Promise<void> {
    const secret = this.#secret;
    this.#secret = null;
    this.#user = undefined;
    this.#progress = undefined;
    try {
      await this.#d.secrets.clear();
    } catch {
      this.#log.log('warn', 'cloud', 'the lab key file could not be deleted');
    }
    await this.#d.setLinked(false);
    this.#changed();
    if (reason === 'removed') {
      this.#log.log('warn', 'cloud', 'the lab connection was removed or expired', { code: CODE_LINK_REMOVED, hadToken: secret !== null });
      this.#d.onRemoved();
    }
  }

  /**
   * Disconnect: revoke the PAT (`DELETE /api/pat/{tokenId}`) when the cloud can be reached, delete the token file and
   * set `linked: false`. When the node is not online the cloud is closed to this app, so the revoke is skipped (the key
   * can be revoked from the Access Tokens page of the cloud); the local side is always cleaned.
   */
  async unlink(): Promise<{ revoked: boolean }> {
    this.#link?.controller.abort();
    const secret = this.#secret;
    let revoked = false;
    if (secret && this.connectedModeActive()) {
      try {
        const result = await this.#call(`/api/pat/${encodeURIComponent(secret.tokenId)}`, { method: 'DELETE', bearer: secret.token }, undefined, 5000);
        // 204: revoked now. 404: already revoked. 401: the token was dead anyway.
        revoked = result.status === 204 || result.status === 200 || result.status === 404 || result.status === 401;
      } catch {
        revoked = false;
      }
    }
    await this.#forget('user');
    this.#log.log('info', 'cloud', 'unlinked from the lab', { revoked });
    return { revoked };
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// The link window (the dialog that shows the code)
// ---------------------------------------------------------------------------------------------------------------------

export interface LinkWindowLike {
  isDestroyed(): boolean;
  isMinimized(): boolean;
  restore(): void;
  show(): void;
  focus(): void;
  close(): void;
  onClosed(listener: () => void): void;
}

export interface LinkWindowFactory {
  /** Creates the window and loads `localai://app/link/` in it. */
  create(init: { title: string; width: number; height: number }): LinkWindowLike;
}

export const LINK_WINDOW = { width: 520, height: 640 } as const;

/** One link window at most; closing it cancels the attempt behind it. */
export class LinkWindowController {
  readonly #factory: LinkWindowFactory;
  readonly #title: () => string;
  readonly #onClosed: () => void;
  #window: LinkWindowLike | null = null;

  constructor(deps: { factory: LinkWindowFactory; title(): string; onClosed(): void }) {
    this.#factory = deps.factory;
    this.#title = deps.title;
    this.#onClosed = deps.onClosed;
  }

  get isOpen(): boolean {
    return this.#window !== null && !this.#window.isDestroyed();
  }

  open(): void {
    const existing = this.#window;
    if (existing && !existing.isDestroyed()) {
      if (existing.isMinimized()) existing.restore();
      existing.show();
      existing.focus();
      return;
    }
    const created = this.#factory.create({ title: this.#title(), width: LINK_WINDOW.width, height: LINK_WINDOW.height });
    this.#window = created;
    created.onClosed(() => {
      if (this.#window === created) this.#window = null;
      this.#onClosed();
    });
  }

  close(): void {
    if (this.#window && !this.#window.isDestroyed()) this.#window.close();
  }
}
