// Egress guard: the two layers that decide what ai-desktop may connect to (docs/specs/desktop.md sections 3.6, 4.9, 6).
//
//   1. Session layer: `webRequest.onBeforeRequest` on every session a renderer can use (default, persist:agent,
//      persist:lab and the updater session). A renderer may reach only localai:, data:, blob:, devtools: (dev only)
//      and http/ws://127.0.0.1:7070. Nothing else, ever, from any renderer.
//   2. Main-process layer: a global undici dispatcher, a wrapped `fetch` and a wrapped `net.Socket.prototype.connect`
//      (which `http`, `https` and `tls` all go through). Loopback and the agent's Unix socket are always allowed;
//      `ai.marin.cr:443` only while connected mode is active; the GitHub release hosts only while "Check for
//      updates" is on and only for code running under `runAsUpdater`.
//
// Strict by default: connected = false, updates = false. Only `setEgressState` changes that, and only DSK-10 and
// DSK-12 call it. Every block is logged at error level with the HOST ONLY (never a path or a query) and counted;
// `blockedTotal()` feeds `--diagnose` (DSK-03). Doctor code LAI-DSK-012 (informational: the protection working).
//
// The module imports only types from `electron`, so it loads under Vitest, where `electron` is a stub.
import { AsyncLocalStorage } from 'node:async_hooks';
import net from 'node:net';
import { Agent, setGlobalDispatcher } from 'undici';
import type { Session } from 'electron';

export const MODULE = 'egress-guard' as const;

/** The agent's loopback address and port (overview.md canonical names). */
export const AGENT_HOST = '127.0.0.1';
export const AGENT_PORT = 7070;
/** The agent's Unix socket: the only Unix socket the main process may open. */
export const AGENT_SOCKET = '/run/localai/agent.sock';
/** The cloud app, reachable only in connected mode, on 443. */
export const CLOUD_HOST = 'ai.marin.cr';
export const CLOUD_PORT = 443;
/** GitHub release hosts, reachable only for the updater while "Check for updates" is on (443). */
export const UPDATE_HOSTS: readonly string[] = ['github.com', 'objects.githubusercontent.com', 'release-assets.githubusercontent.com'];
export const DOCTOR_CODE = 'LAI-DSK-012' as const;

/** Session partitions the renderers and the updater use. The default session needs no name. */
export const PARTITIONS = {
  agent: 'persist:agent',
  lab: 'persist:lab',
  /** In memory on purpose: the updater session keeps no cookies or cache on disk. Used by DSK-12. */
  updater: 'updater',
} as const;

// ---------------------------------------------------------------------------------------------------------------------
// State, counter, logging
// ---------------------------------------------------------------------------------------------------------------------

export interface EgressState {
  /** Connected mode is active (desktop.md 4.9): linked, a token is held and the node's mode is `online`. */
  connected: boolean;
  /** "Check for updates" is on AND the node is allowed to update (not offline/survival). The caller computes it. */
  updates: boolean;
}

let state: EgressState = { connected: false, updates: false };
let blocked = 0;

/** Switches connected mode and the updater allowance. The only way the strict defaults are ever relaxed. */
export function setEgressState(next: EgressState): void {
  if (typeof next?.connected !== 'boolean' || typeof next?.updates !== 'boolean') {
    throw new TypeError('setEgressState needs { connected: boolean, updates: boolean }');
  }
  state = { connected: next.connected, updates: next.updates };
}

/**
 * Changes one half of the state and keeps the other: connected mode (DSK-10, `cloud-link.ts`) and the updater
 * allowance (DSK-12) are set by different owners, so neither may overwrite the other with a stale copy.
 */
export function patchEgressState(patch: Partial<EgressState>): void {
  setEgressState({ connected: patch.connected ?? state.connected, updates: patch.updates ?? state.updates });
}

export function getEgressState(): EgressState {
  return { ...state };
}

/** How many connections the guard has blocked since the process started (both layers). */
export function blockedTotal(): number {
  return blocked;
}

export interface EgressLogEntry {
  ts: string;
  level: 'error';
  msg: string;
  component: 'egress';
  code: typeof DOCTOR_CODE;
  host: string;
  blocked_total: number;
}
export type EgressLogger = (entry: EgressLogEntry) => void;

const defaultLogger: EgressLogger = (entry) => {
  process.stderr.write(`${JSON.stringify(entry)}\n`);
};
let logger: EgressLogger = defaultLogger;

/** The file logger of a later item replaces the stderr default here. */
export function setEgressLogger(next: EgressLogger | null): void {
  logger = next ?? defaultLogger;
}

/** Counts one block and logs it. `host` is a host name or an address, never a URL. */
function recordBlock(host: string): void {
  blocked += 1;
  try {
    logger({
      ts: new Date().toISOString(),
      level: 'error',
      msg: 'blocked a connection to the internet',
      component: 'egress',
      code: DOCTOR_CODE,
      host,
      blocked_total: blocked,
    });
  } catch {
    // A broken logger must never turn a block into a crash or an allow.
  }
}

/** Thrown by `fetch` (and delivered as the `error` of a socket) when the main-process layer blocks a connection. */
export class EgressBlockedError extends Error {
  readonly code = DOCTOR_CODE;
  readonly host: string;
  constructor(host: string) {
    super(`ai-desktop blocked a connection to ${host} (${DOCTOR_CODE})`);
    this.name = 'EgressBlockedError';
    this.host = host;
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// Host matching
// ---------------------------------------------------------------------------------------------------------------------

/** Lowercase, drop IPv6 brackets and zone, drop ONE trailing dot. Matching is exact after this (implementation note 2). */
export function normalizeHost(raw: string): string {
  let host = String(raw).trim().toLowerCase();
  if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1);
  const zone = host.indexOf('%');
  if (zone !== -1) host = host.slice(0, zone);
  if (host.endsWith('.')) host = host.slice(0, -1);
  return host;
}

/** 127.0.0.0/8, ::1, ::ffff:127.x.x.x (also written in hex) and the name `localhost`. Anything unparsable is not loopback. */
export function isLoopbackHost(raw: string): boolean {
  const host = normalizeHost(raw);
  if (host === 'localhost') return true;
  if (net.isIPv4(host)) return host.startsWith('127.');
  if (net.isIPv6(host)) {
    let canonical: string;
    try {
      // The URL parser canonicalises every spelling: ::ffff:127.0.0.1 -> ::ffff:7f00:1, 0:0:0:0:0:0:0:1 -> ::1.
      canonical = new URL(`http://[${host}]/`).hostname.slice(1, -1);
    } catch {
      return false;
    }
    return canonical === '::1' || /^::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}$/.test(canonical);
  }
  return false;
}

/** The port as a number, or null when it is missing or not 1..65535. */
function toPort(raw: unknown): number | null {
  if (raw === undefined || raw === null || raw === '') return null;
  const port = typeof raw === 'number' ? raw : /^\d{1,5}$/.test(String(raw)) ? Number(raw) : Number.NaN;
  return Number.isInteger(port) && port >= 1 && port <= 65535 ? port : null;
}

// ---------------------------------------------------------------------------------------------------------------------
// Main-process layer: decisions
// ---------------------------------------------------------------------------------------------------------------------

/** Who is asking. Only code that runs under `runAsUpdater` is the updater. */
export type Caller = 'app' | 'updater';

const callerStore = new AsyncLocalStorage<Caller>();

/** Runs `fn` (and everything it awaits) as the updater, which is the only caller allowed to reach the GitHub hosts. */
export function runAsUpdater<T>(fn: () => T): T {
  return callerStore.run('updater', fn);
}

function currentCaller(): Caller {
  return callerStore.getStore() ?? 'app';
}

export type EgressTarget =
  | { kind: 'tcp'; host: string; port: unknown }
  | { kind: 'unix'; path: string };

export interface EgressDecision {
  allow: boolean;
  /** What to log when blocked: a host name or address, or `unix-socket`. Never a path or a query. */
  label: string;
}

export interface MainEgressOptions {
  state?: EgressState;
  caller?: Caller;
  unixSockets?: readonly string[];
}

/** Host and port against the main-process allowlist (no URL scheme involved; sockets know none). */
export function decideMainEgress(target: EgressTarget, options: MainEgressOptions = {}): EgressDecision {
  const current = options.state ?? state;
  const caller = options.caller ?? currentCaller();
  if (target.kind === 'unix') {
    const allowed = (options.unixSockets ?? [AGENT_SOCKET]).includes(target.path);
    return { allow: allowed, label: 'unix-socket' };
  }
  const host = normalizeHost(target.host);
  if (host === '') return { allow: false, label: '(none)' };
  if (isLoopbackHost(host)) return { allow: true, label: host };
  const port = toPort(target.port);
  if (port === null) return { allow: false, label: host };
  if (current.connected && host === CLOUD_HOST && port === CLOUD_PORT) return { allow: true, label: host };
  if (current.updates && caller === 'updater' && port === 443 && UPDATE_HOSTS.includes(host)) return { allow: true, label: host };
  return { allow: false, label: host };
}

/**
 * A URL against the main-process allowlist. Remote hosts must be reached over TLS (`https:` / `wss:`), so
 * `http://ai.marin.cr` is denied even in connected mode. `data:` and `blob:` open no connection and pass.
 */
export function decideMainUrl(raw: string | URL, options: MainEgressOptions = {}): EgressDecision {
  let url: URL;
  try {
    url = raw instanceof URL ? raw : new URL(raw);
  } catch {
    return { allow: false, label: '(invalid url)' };
  }
  if (url.protocol === 'data:' || url.protocol === 'blob:') return { allow: true, label: url.protocol };
  const secure = url.protocol === 'https:' || url.protocol === 'wss:';
  if (!secure && url.protocol !== 'http:' && url.protocol !== 'ws:') {
    return { allow: false, label: url.hostname !== '' ? normalizeHost(url.hostname) : url.protocol };
  }
  const defaultPort = secure ? 443 : 80;
  const decision = decideMainEgress({ kind: 'tcp', host: url.hostname, port: url.port === '' ? defaultPort : url.port }, options);
  if (!decision.allow) return decision;
  if (!secure && !isLoopbackHost(url.hostname)) return { allow: false, label: decision.label };
  return decision;
}

// ---------------------------------------------------------------------------------------------------------------------
// Main-process layer: installation
// ---------------------------------------------------------------------------------------------------------------------

interface Restore {
  fetch: typeof globalThis.fetch;
  connect: typeof net.Socket.prototype.connect;
}
let restore: Restore | null = null;

function inputUrl(input: unknown): string | URL | null {
  if (typeof input === 'string' || input instanceof URL) return input;
  if (input && typeof (input as { url?: unknown }).url === 'string') return (input as { url: string }).url;
  return null;
}

/** The target of a `socket.connect(...)` call, in any of the shapes Node accepts. Null when it cannot be read. */
export function socketTarget(args: unknown[]): EgressTarget | null {
  let first: unknown = args[0];
  // net.createConnection hands connect() a normalised `[options, callback]` array.
  if (Array.isArray(first)) first = first[0];
  if (first !== null && typeof first === 'object') {
    const options = first as { path?: unknown; host?: unknown; port?: unknown };
    if (typeof options.path === 'string') return { kind: 'unix', path: options.path };
    if (options.port === undefined || options.port === null) return null; // e.g. an fd or a handle: not a network target
    return { kind: 'tcp', host: typeof options.host === 'string' && options.host !== '' ? options.host : 'localhost', port: options.port };
  }
  if (typeof first === 'number' || (typeof first === 'string' && /^\d+$/.test(first))) {
    return { kind: 'tcp', host: typeof args[1] === 'string' ? args[1] : 'localhost', port: first };
  }
  if (typeof first === 'string') return { kind: 'unix', path: first };
  return null;
}

/**
 * Installs the main-process layer: a global undici dispatcher, a wrapped `fetch` and a wrapped
 * `net.Socket.prototype.connect`. Idempotent. Call it before anything that can open a connection.
 */
export function installMainProcessGuard(): void {
  if (restore) return;
  restore = { fetch: globalThis.fetch, connect: net.Socket.prototype.connect };

  // 1. fetch: reject with EgressBlockedError itself (the dispatcher below would surface it only as `cause`).
  const originalFetch = globalThis.fetch;
  globalThis.fetch = function guardedFetch(input: Parameters<typeof fetch>[0], init?: Parameters<typeof fetch>[1]) {
    const url = inputUrl(input);
    if (url === null) return Promise.reject(new EgressBlockedError('(invalid url)'));
    const decision = decideMainUrl(url);
    if (!decision.allow) {
      recordBlock(decision.label);
      return Promise.reject(new EgressBlockedError(decision.label));
    }
    return originalFetch(input, init);
  } as typeof fetch;

  // 2. undici: every request fetch makes, redirects included, passes the dispatcher.
  setGlobalDispatcher(
    new Agent().compose((dispatch) => (opts, handler) => {
      const origin = typeof opts.origin === 'string' ? opts.origin : opts.origin?.toString();
      const decision = origin ? decideMainUrl(origin) : { allow: false, label: '(invalid url)' };
      if (!decision.allow) {
        recordBlock(decision.label);
        throw new EgressBlockedError(decision.label);
      }
      return dispatch(opts, handler);
    }),
  );

  // 3. sockets: whatever else connects (http, https, tls, a library) ends in net.Socket.prototype.connect.
  const originalConnect = net.Socket.prototype.connect;
  net.Socket.prototype.connect = function guardedConnect(this: net.Socket, ...args: unknown[]) {
    const target = socketTarget(args);
    const decision = target ? decideMainEgress(target) : { allow: false, label: '(unknown)' };
    if (!decision.allow) {
      recordBlock(decision.label);
      const error = new EgressBlockedError(decision.label);
      process.nextTick(() => this.destroy(error));
      return this;
    }
    return (originalConnect as (...a: unknown[]) => net.Socket).apply(this, args);
  } as typeof net.Socket.prototype.connect;
}

/** Test helper: puts the original `fetch` and `connect` back and resets the state and the counter. */
export function resetEgressGuardForTests(): void {
  if (restore) {
    globalThis.fetch = restore.fetch;
    net.Socket.prototype.connect = restore.connect;
    restore = null;
  }
  state = { connected: false, updates: false };
  blocked = 0;
  logger = defaultLogger;
  sessionsGuarded.clear();
}

// ---------------------------------------------------------------------------------------------------------------------
// Session layer
// ---------------------------------------------------------------------------------------------------------------------

export interface SessionGuardOptions {
  /** `updater` additionally lets the GitHub hosts through while `state.updates` is on. */
  kind: 'renderer' | 'updater';
  /** True in an unpackaged build: allows `devtools:` and the Vite dev server's origin. */
  dev?: boolean;
  /** `http://localhost:5173`-style origin of the Vite dev server (unpackaged builds only). */
  devOrigin?: string;
  state?: EgressState;
}

/** The session-layer allowlist. Pure; the hook below only applies it. */
export function decideSessionRequest(raw: string, options: SessionGuardOptions): EgressDecision {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return { allow: false, label: '(invalid url)' };
  }
  const label = url.hostname !== '' ? normalizeHost(url.hostname) : url.protocol;
  const deny: EgressDecision = { allow: false, label };
  const allow: EgressDecision = { allow: true, label };
  switch (url.protocol) {
    case 'localai:':
    case 'data:':
    case 'blob:':
      return allow;
    case 'devtools:':
      return options.dev ? allow : deny;
    case 'http:':
    case 'ws:': {
      if (url.username !== '' || url.password !== '') return deny;
      if (url.hostname === AGENT_HOST && url.port === String(AGENT_PORT)) return allow;
      if (options.dev && options.devOrigin) {
        try {
          const dev = new URL(options.devOrigin);
          const sameHost = normalizeHost(url.hostname) === normalizeHost(dev.hostname) && url.port === dev.port;
          if (sameHost && isLoopbackHost(url.hostname)) return allow;
        } catch {
          // an unusable dev origin allows nothing
        }
      }
      return deny;
    }
    case 'https:': {
      const current = options.state ?? state;
      const host = normalizeHost(url.hostname);
      const port = url.port === '' ? 443 : Number(url.port);
      if (options.kind === 'updater' && current.updates && port === 443 && UPDATE_HOSTS.includes(host) && url.username === '' && url.password === '') {
        return allow;
      }
      return deny;
    }
    default:
      return deny;
  }
}

type WebRequestHost = Pick<Session, 'webRequest'>;
const sessionsGuarded = new Set<WebRequestHost>();

/** Installs `webRequest.onBeforeRequest` on one session. There is one such listener per session: nothing else may set it. */
export function installSessionGuard(ses: WebRequestHost, options: SessionGuardOptions): void {
  ses.webRequest.onBeforeRequest((details, callback) => {
    const decision = decideSessionRequest(details.url, options);
    if (!decision.allow) recordBlock(decision.label);
    callback({ cancel: !decision.allow });
  });
  sessionsGuarded.add(ses);
}

export interface GuardedSessions {
  default: Session;
  agent: Session;
  lab: Session;
  updater: Session;
}

export interface InstallEgressGuardOptions {
  /** `electron.session`. */
  session: { defaultSession: Session; fromPartition(partition: string): Session };
  isPackaged: boolean;
  /** `process.env.ELECTRON_RENDERER_URL`: honoured only when not packaged. */
  devRendererUrl?: string;
}

let guardsInstalled = false;
let guardedSessions: GuardedSessions | null = null;

/**
 * Installs both layers, before any window exists: the main-process layer, then the session layer on the default
 * session, `persist:agent`, `persist:lab` and the updater session (created here, blocked by default; DSK-12 uses it).
 */
export function installEgressGuard(options: InstallEgressGuardOptions): GuardedSessions {
  installMainProcessGuard();
  const dev = !options.isPackaged;
  const devOrigin = dev ? options.devRendererUrl : undefined;
  const make = (ses: Session, kind: SessionGuardOptions['kind']): Session => {
    installSessionGuard(ses, { kind, dev, devOrigin });
    return ses;
  };
  guardedSessions = {
    default: make(options.session.defaultSession, 'renderer'),
    agent: make(options.session.fromPartition(PARTITIONS.agent), 'renderer'),
    lab: make(options.session.fromPartition(PARTITIONS.lab), 'renderer'),
    updater: make(options.session.fromPartition(PARTITIONS.updater), 'updater'),
  };
  guardsInstalled = true;
  return guardedSessions;
}

/** `egressGuard.installed` is true only after `installEgressGuard` finished: windows are created after that. */
export const egressGuard = {
  get installed(): boolean {
    return guardsInstalled && restore !== null;
  },
  get sessions(): GuardedSessions | null {
    return guardedSessions;
  },
};

/** The updater session (DSK-12 hands it to electron-updater). Throws before the guard is installed. */
export function getUpdaterSession(): Session {
  if (!guardedSessions) throw new Error('the egress guard is not installed yet');
  return guardedSessions.updater;
}

// ---------------------------------------------------------------------------------------------------------------------
// Chromium process switches (desktop.md 3.3, last paragraph)
// ---------------------------------------------------------------------------------------------------------------------

/** Switches that stop Chromium's own background networking. Set before `app.whenReady()`. */
export const PROCESS_SWITCHES: ReadonlyArray<readonly [string, string?]> = [
  ['disable-background-networking'],
  ['no-pings'],
  ['disable-component-update'],
  ['disable-domain-reliability'],
  ['disable-features', 'AutofillServerCommunication,MediaRouter,OptimizationHints,Translate'],
];

export function applyProcessSwitches(app: { commandLine: { appendSwitch(name: string, value?: string): void } }): void {
  for (const [name, value] of PROCESS_SWITCHES) {
    if (value === undefined) app.commandLine.appendSwitch(name);
    else app.commandLine.appendSwitch(name, value);
  }
}
