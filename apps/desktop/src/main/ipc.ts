// The whole IPC surface between pages and the main process (docs/specs/desktop.md sections 3.5 and 6).
//
// Every channel is declared in a handler table; `registerHandlers` wraps each entry so that NOTHING runs unless the
// calling frame is the main frame of an allowed origin: `localai://app` for the shell bridge, `http://127.0.0.1:7070`
// for the host bridge (plus the Vite dev origin for shell pages in an unpackaged build). A call from anywhere else
// (a `data:` page, a `file:` page, `localhost:7070`, the cloud) is refused before any argument is read and has no side
// effect. Arguments are validated here too: a page never gets to pass a path, a URL or a free-form setting.
import { mkdir } from 'node:fs/promises';
import { AGENT_HOST, AGENT_PORT, isLoopbackHost } from './egress-guard';
import { ERROR_ACTION_CHANNEL, type Lang } from './i18n';
import { NULL_LOGGER, type Logger } from './log';
import { readSettings, updateSettings, type Settings, type SettingsPatch } from './settings';
import { handleErrorAction, isErrorPageUrl, parseActionRequest, type ActionDeps } from './startup';
import type { AgentStatus, ConnectionState } from './agent-client';
import type {
  ActionResult,
  AgentReach,
  ChildWindowName,
  CloudLinkState,
  CloudResult,
  ConnectionInfoResult,
  CopyKeyResult,
  EnsureResult,
  LabCacheView,
  DesktopStatus,
  LinkBegin,
  LinkRefusal,
  NodeMode,
  NotImplemented,
  PinResult,
  RevealResult,
  RotateKeyResult,
  ShellSettings,
  ShellSettingsPatch,
  SurvivalState,
  MusicAvailability,
  Tab,
  WindowBounds,
} from '../shared/types';

export const MODULE = 'ipc' as const;

// ---------------------------------------------------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------------------------------------------------

/** The preloads repeat these strings (a sandboxed preload cannot import a shared chunk); tests/unit/preload.test.ts proves they agree. */
export const CHANNELS = {
  status: 'localai:status',
  statusChanged: 'localai:status-changed',
  notice: 'localai:notice',
  startAgent: 'localai:start-agent',
  copyApiKey: 'localai:copy-api-key',
  connectionInfo: 'localai:connection-info',
  revealKey: 'localai:reveal-key',
  setMode: 'localai:set-mode',
  openWindow: 'localai:open-window',
  showTab: 'localai:show-tab',
  setFullScreen: 'localai:set-full-screen',
  closePortal: 'localai:close-portal',
  showMenu: 'localai:show-menu',
  cloudBeginLink: 'localai:cloud-begin-link',
  cloudOpenVerification: 'localai:cloud-open-verification',
  cloudLinkState: 'localai:cloud-link-state',
  cloudUnlink: 'localai:cloud-unlink',
  cloudRequest: 'localai:cloud-request',
  cloudCached: 'localai:cloud-cached',
  cloudOpenNode: 'localai:cloud-open-node',
  settingsGet: 'localai:settings-get',
  settingsSet: 'localai:settings-set',
  pinSubmit: 'localai:pin-submit',
  pinCancel: 'localai:pin-cancel',
  rotateKey: 'localai:rotate-key',
  ensureView: 'localai:ensure-view',
  ensureStart: 'localai:ensure-start',
  ensureCancel: 'localai:ensure-cancel',
  ensureConfirm: 'localai:ensure-confirm',
  lang: 'localai:lang',
  errorAction: ERROR_ACTION_CHANNEL,
  hostCopyApiKey: 'localai:host:copy-api-key',
  hostOpenWindow: 'localai:host:open-window',
  hostConnectionInfo: 'localai:host:connection-info',
  hostVersion: 'localai:host:version',
} as const;

// ---------------------------------------------------------------------------------------------------------------------
// Origin check
// ---------------------------------------------------------------------------------------------------------------------

export type OriginKind = 'shell' | 'host';

export interface OriginPolicy {
  /** Unpackaged builds only: the Vite dev server's origin, which shell pages are served from. */
  devOrigin?: string;
}

const AGENT_ORIGIN = `http://${AGENT_HOST}:${AGENT_PORT}`;

function parse(raw: string): URL | null {
  try {
    return new URL(raw);
  } catch {
    return null;
  }
}

/** Is `url` a page of the origin that `kind` of handler serves? Exact origin, no credentials; the path does not matter. */
export function isAllowedOrigin(kind: OriginKind, url: string, policy: OriginPolicy = {}): boolean {
  const parsed = parse(url);
  if (!parsed || parsed.username !== '' || parsed.password !== '') return false;
  if (kind === 'host') return parsed.protocol === 'http:' && parsed.origin === AGENT_ORIGIN;
  if (parsed.protocol === 'localai:') return parsed.hostname === 'app' && parsed.port === '';
  if (policy.devOrigin) {
    const dev = parse(policy.devOrigin);
    return dev !== null && parsed.origin === dev.origin && isLoopbackHost(parsed.hostname);
  }
  return false;
}

/** Scheme and host only, for the log: never a path or a query. */
export function describeOrigin(url: string): string {
  const parsed = parse(url);
  return parsed ? `${parsed.protocol}//${parsed.host}` : 'invalid-url';
}

/** The part of an IPC event the checks read. Electron's `IpcMainEvent` and `IpcMainInvokeEvent` both satisfy it. */
export interface SenderEvent {
  sender: { id: number };
  senderFrame: { url: string; parent: unknown } | null;
  returnValue?: unknown;
}

/** True only for a live main frame (not an iframe) whose URL passes the origin check. */
export function senderAllowed(kind: OriginKind, event: SenderEvent, policy: OriginPolicy = {}): boolean {
  const frame = event.senderFrame;
  if (!frame || frame.parent !== null) return false;
  return isAllowedOrigin(kind, frame.url, policy);
}

export class IpcRejectedError extends Error {
  constructor(channel: string) {
    super(`${channel}: this page may not call it`);
    this.name = 'IpcRejectedError';
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// Handler table and registration
// ---------------------------------------------------------------------------------------------------------------------

/** `invoke` (a promise), `send` (fire and forget) and `sync` (`sendSync`, answered through `event.returnValue`). */
export type HandlerKind = 'invoke' | 'send' | 'sync';

export interface HandlerSpec {
  kind: HandlerKind;
  origin: OriginKind;
  /** A further restriction on the caller (one page, one window); runs after the origin check. */
  accept?(event: SenderEvent): boolean;
  /** Receives the validated-by-the-handler arguments; a `sync` handler returns the value for the page. */
  run(event: SenderEvent, ...args: unknown[]): unknown;
}

export type HandlerTable = Record<string, HandlerSpec>;

/** The two `ipcMain` methods the registration uses. */
export interface IpcLike {
  // `any`: Electron's own event types differ per method; the wrapper below narrows them to `SenderEvent`.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handle(channel: string, listener: (event: any, ...args: any[]) => unknown): void;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  on(channel: string, listener: (event: any, ...args: any[]) => void): void;
}

export interface RegisterOptions {
  policy: OriginPolicy;
  log?: Logger;
}

/** Wraps every entry with the origin check. Returns the wrapped functions too, so a test can call them without Electron. */
export function registerHandlers(ipc: IpcLike, table: HandlerTable, options: RegisterOptions): Record<string, (event: SenderEvent, ...args: unknown[]) => unknown> {
  const log = options.log ?? NULL_LOGGER;
  const wrapped: Record<string, (event: SenderEvent, ...args: unknown[]) => unknown> = {};
  for (const [channel, spec] of Object.entries(table)) {
    const allowed = (event: SenderEvent): boolean => {
      const ok = senderAllowed(spec.origin, event, options.policy) && (spec.accept ? spec.accept(event) : true);
      if (!ok) log.log('warn', 'app', 'an IPC call was refused', { channel, origin: describeOrigin(event.senderFrame?.url ?? '') });
      return ok;
    };
    if (spec.kind === 'invoke') {
      const listener = async (event: SenderEvent, ...args: unknown[]): Promise<unknown> => {
        if (!allowed(event)) throw new IpcRejectedError(channel);
        return spec.run(event, ...args);
      };
      wrapped[channel] = listener;
      ipc.handle(channel, listener);
    } else if (spec.kind === 'send') {
      const listener = (event: SenderEvent, ...args: unknown[]): void => {
        if (!allowed(event)) return;
        try {
          void Promise.resolve(spec.run(event, ...args)).catch(() => undefined);
        } catch {
          // A handler that throws on bad arguments does nothing; there is nobody to tell.
        }
      };
      wrapped[channel] = listener;
      ipc.on(channel, listener);
    } else {
      const listener = (event: SenderEvent, ...args: unknown[]): void => {
        event.returnValue = allowed(event) ? spec.run(event, ...args) : null;
      };
      wrapped[channel] = listener;
      ipc.on(channel, listener);
    }
  }
  return wrapped;
}

// ---------------------------------------------------------------------------------------------------------------------
// Settings: what a page may read and change
// ---------------------------------------------------------------------------------------------------------------------

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const BOUNDS_KEYS = ['x', 'y', 'width', 'height', 'maximized'] as const;
const COORDINATE_LIMIT = 100_000;

/** A saved or submitted window rectangle: finite integers in a sane range, a size above zero. Anything else is dropped. */
export function sanitizeBounds(raw: unknown): WindowBounds {
  const out: WindowBounds = {};
  if (!isObject(raw)) return out;
  for (const key of ['x', 'y'] as const) {
    const v = raw[key];
    if (typeof v === 'number' && Number.isFinite(v) && Math.abs(v) <= COORDINATE_LIMIT) out[key] = Math.round(v);
  }
  for (const key of ['width', 'height'] as const) {
    const v = raw[key];
    if (typeof v === 'number' && Number.isFinite(v) && v > 0 && v <= COORDINATE_LIMIT) out[key] = Math.round(v);
  }
  if (typeof raw['maximized'] === 'boolean') out.maximized = raw['maximized'];
  return out;
}

/** The part of the settings file a shell page sees: known keys only, bounds sanitised. */
export function projectSettings(settings: Settings): ShellSettings {
  return {
    autostart: settings.autostart,
    updates: { checkForUpdates: settings.updates.checkForUpdates },
    lang: settings.lang,
    window: { bounds: sanitizeBounds(settings.window.bounds) },
    secrets: { allowPlaintext: settings.secrets.allowPlaintext },
    cloud: { url: settings.cloud.url, linked: settings.cloud.linked },
  };
}

export type PatchResult = { ok: true; patch: SettingsPatch } | { ok: false; reason: string };

function onlyKeys(value: Record<string, unknown>, allowed: readonly string[], where: string): string | null {
  for (const key of Object.keys(value)) if (!allowed.includes(key)) return `${where}${key} may not be changed from a page`;
  return null;
}

/**
 * The allowlist of user-settable keys (implementation note 2): `autostart`, `updates.checkForUpdates`, `lang`,
 * `window.bounds`, `secrets.allowPlaintext`. `cloud.*` and every other key are refused, and so is a value of the wrong
 * type. The result is built key by key from the known names, so nothing the page sent is copied through.
 */
export function validateSettingsPatch(raw: unknown): PatchResult {
  if (!isObject(raw)) return { ok: false, reason: 'the patch must be an object' };
  const bad = onlyKeys(raw, ['autostart', 'updates', 'lang', 'window', 'secrets'], '');
  if (bad) return { ok: false, reason: bad };
  const patch: SettingsPatch = {};
  if ('autostart' in raw) {
    if (typeof raw['autostart'] !== 'boolean') return { ok: false, reason: 'autostart must be true or false' };
    patch.autostart = raw['autostart'];
  }
  if ('lang' in raw) {
    const lang = raw['lang'];
    if (lang !== 'auto' && lang !== 'en' && lang !== 'es') return { ok: false, reason: 'lang must be auto, en or es' };
    patch.lang = lang;
  }
  if ('updates' in raw) {
    const section = raw['updates'];
    if (!isObject(section)) return { ok: false, reason: 'updates must be an object' };
    const err = onlyKeys(section, ['checkForUpdates'], 'updates.');
    if (err) return { ok: false, reason: err };
    if ('checkForUpdates' in section) {
      if (typeof section['checkForUpdates'] !== 'boolean') return { ok: false, reason: 'updates.checkForUpdates must be true or false' };
      patch.updates = { checkForUpdates: section['checkForUpdates'] };
    }
  }
  if ('secrets' in raw) {
    const section = raw['secrets'];
    if (!isObject(section)) return { ok: false, reason: 'secrets must be an object' };
    const err = onlyKeys(section, ['allowPlaintext'], 'secrets.');
    if (err) return { ok: false, reason: err };
    if ('allowPlaintext' in section) {
      if (typeof section['allowPlaintext'] !== 'boolean') return { ok: false, reason: 'secrets.allowPlaintext must be true or false' };
      patch.secrets = { allowPlaintext: section['allowPlaintext'] };
    }
  }
  if ('window' in raw) {
    const section = raw['window'];
    if (!isObject(section)) return { ok: false, reason: 'window must be an object' };
    const err = onlyKeys(section, ['bounds'], 'window.');
    if (err) return { ok: false, reason: err };
    if ('bounds' in section) {
      const bounds = section['bounds'];
      if (!isObject(bounds)) return { ok: false, reason: 'window.bounds must be an object' };
      const extra = onlyKeys(bounds, BOUNDS_KEYS, 'window.bounds.');
      if (extra) return { ok: false, reason: extra };
      const clean = sanitizeBounds(bounds);
      // A value that sanitising dropped was not a valid number or boolean: refuse rather than save a partial rectangle.
      if (Object.keys(clean).length !== Object.keys(bounds).length) return { ok: false, reason: 'window.bounds holds an invalid value' };
      patch.window = { bounds: { ...clean } };
    }
  }
  return { ok: true, patch };
}

export interface SettingsStore {
  get(): Promise<Settings>;
  /** Applies a patch the caller already validated. */
  update(patch: SettingsPatch): Promise<Settings>;
}

/**
 * The settings file behind one queue: a read-modify-write never interleaves with another (the window saves its bounds
 * while a page may be changing a setting). `onChanged` runs after each successful write (autostart file, language, ...).
 */
export function createSettingsStore(dir: string, onChanged?: (next: Settings, patch: SettingsPatch) => void): SettingsStore {
  let chain: Promise<unknown> = Promise.resolve();
  const enqueue = <T>(work: () => Promise<T>): Promise<T> => {
    const run = chain.then(work, work);
    chain = run.catch(() => undefined);
    return run;
  };
  return {
    get: () => enqueue(() => readSettings(dir)),
    update: (patch) =>
      enqueue(async () => {
        await mkdir(dir, { recursive: true, mode: 0o700 });
        const next = await updateSettings(dir, patch);
        try {
          onChanged?.(next, patch);
        } catch {
          // A listener that throws does not undo the write.
        }
        return next;
      }),
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------------------------------------------------

/** `starting` while the start-up sequence runs, `ready` once it showed the agent UI or the Portal, `error` when it ended on an error page. */
export type StartupPhase = 'starting' | 'ready' | 'error';

export interface StatusInputs {
  version: string;
  phase: StartupPhase;
  connection: ConnectionState;
  agentStatus: AgentStatus | null;
  cloud: CloudLinkState;
  tab: Tab;
  /** The survival strip's state (DSK-09); absent: not in survival form. */
  survival?: SurvivalState;
  /** Issue #515: the last answer of `GET /api/v1/music/status` (src/main/music.ts); absent: not read. */
  music?: MusicAvailability;
}

const MODES: readonly string[] = ['online', 'offline', 'survival'];

export function agentReach(phase: StartupPhase, connection: ConnectionState): AgentReach {
  if (phase === 'starting') return 'starting';
  if (phase === 'error') return 'down';
  switch (connection) {
    case 'connected':
      return 'up';
    case 'connecting':
      return 'starting';
    case 'unauthorized':
      return 'unauthorized';
    default:
      return 'down';
  }
}

const ALERT_SEVERITIES: readonly string[] = ['warning', 'error', 'critical'];

/** The snapshot a page gets from `status()` and from every `onStatus` push. */
export function buildDesktopStatus(input: StatusInputs): DesktopStatus {
  const raw = input.agentStatus?.mode?.value;
  const mode: NodeMode | null = typeof raw === 'string' && MODES.includes(raw) ? (raw as NodeMode) : null;
  const connectivity = (input.agentStatus as { connectivity?: { value?: unknown } } | null)?.connectivity?.value;
  const alerts = (input.agentStatus?.alerts ?? []).filter((a) => typeof a.severity === 'string' && ALERT_SEVERITIES.includes(a.severity)).length;
  return {
    version: input.version,
    agent: agentReach(input.phase, input.connection),
    mode,
    connectivity: typeof connectivity === 'string' ? connectivity : null,
    alerts,
    cloud: { ...input.cloud },
    // desktop.md 4.9: linked AND a token held AND the node's mode is online.
    connected: input.cloud.linked && input.cloud.tokenHeld && mode === 'online',
    tab: input.tab,
    ...(input.survival !== undefined ? { survival: input.survival } : {}),
    // Only what an agent that is up said counts: a stale "installed" must not survive the agent going down.
    music: agentReach(input.phase, input.connection) === 'up' ? (input.music ?? 'unknown') : 'unknown',
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// The handler tables
// ---------------------------------------------------------------------------------------------------------------------

const NOT_IMPLEMENTED: NotImplemented = { ok: false, code: 'not_implemented' };
const CHILD_NAMES: readonly string[] = ['chat', 'library', 'maps', 'music'];
const TABS: readonly string[] = ['local', 'lab'];
const SET_MODE_TARGETS: readonly string[] = ['online', 'offline', 'survival'];

/** The key calls behind `copyApiKey`, `connectionInfo`, `revealKey` and `rotateKey` (src/main/clipboard.ts, index.ts). */
export interface KeyActions {
  copy(): Promise<CopyKeyResult>;
  /** Everything the connection-info page shows; the key is masked. */
  info(): Promise<ConnectionInfoResult>;
  /** The full key. Re-reads `GET /key`, so the agent audits each Reveal. */
  reveal(): Promise<RevealResult>;
  /** Asks for the PIN, then rotates with the grant of that one request. */
  rotate(): Promise<RotateKeyResult>;
}

/** The ensure window's calls (src/main/ensure.ts, ADR-0010). `confirm` receives whatever the page sent and validates it. */
export interface EnsureActions {
  view(): Promise<EnsureResult>;
  start(): Promise<EnsureResult>;
  cancel(): Promise<EnsureResult>;
  /** Asks for the PIN, then starts the pass with the steps the person approved. */
  confirm(steps: unknown): Promise<EnsureResult>;
}

/** The PIN window's calls (src/main/pin.ts). `senderId` is the calling webContents. */
export interface PinActions {
  submit(senderId: number, pin: unknown): Promise<PinResult>;
  cancel(senderId: number): void;
}

/** Is `url` the shell page `page` (`localai://app/<page>/`, or the Vite dev origin's `/<page>/...` when unpackaged)? */
export function isShellPage(url: string, page: string, devOrigin?: string): boolean {
  const parsed = parse(url);
  if (!parsed || parsed.username !== '' || parsed.password !== '') return false;
  const onPage = (path: string): boolean => path === `/${page}` || path.startsWith(`/${page}/`);
  if (parsed.protocol === 'localai:') return parsed.hostname === 'app' && onPage(parsed.pathname);
  const dev = devOrigin ? parse(devOrigin) : null;
  return dev !== null && parsed.origin === dev.origin && onPage(parsed.pathname);
}

/** What the bridge's `cloud.*` members need from the cloud session (cloud-link.ts, DSK-10). */
export interface CloudBridge {
  state(): CloudLinkState;
  beginLink(): Promise<LinkBegin | LinkRefusal>;
  openVerification(): void;
  unlink(): Promise<unknown>;
}

/** What the Lab's `cloud.request/cached/openNode` need (DSK-11: cloud-api.ts's proxy, and the browser). */
export interface LabBridge {
  /** The allowlisted proxy; `raw` is whatever the page sent. */
  request(raw: unknown): Promise<CloudResult>;
  cached(): Promise<LabCacheView | null>;
  /** Opens the node's page in the cloud in the default browser; the id is checked by the implementation. */
  openNode(nodeId: unknown): void;
}

export interface BridgeDeps {
  policy: OriginPolicy;
  version: string;
  lang(): Lang;
  status(): DesktopStatus;
  openChild(name: ChildWindowName): void;
  showTab(tab: Tab): void;
  /** Survival strip (DSK-09). Absent: the channels do nothing. */
  setFullScreen?(flag: boolean): void;
  closePortal?(): void;
  /** Pops up the top bar's native menu. */
  showMenu(): void;
  settings: SettingsStore;
  /** The lab link (DSK-10). Absent: `beginLink` answers `unavailable` and `linkState` the status's `cloud`. */
  cloud?: CloudBridge;
  /** The Lab's cloud proxy (DSK-11). Absent: `cloud.request` answers `{ ok: false, error: 'unavailable' }`. */
  lab?: LabBridge;
  /** Switches the node's mode (DSK-06's `ModeController`, the same path as the tray). Absent: `setMode` answers `not_implemented`. */
  setMode?(target: NodeMode): Promise<ActionResult>;
  /** The window id of the PIN prompt (DSK-08), or null while there is none: `pinSubmit` is refused from every other page. */
  pinWindowId(): number | null;
  /** The key calls (DSK-08). Absent: those channels answer `not_implemented`. */
  keys?: KeyActions;
  /** The PIN window's calls (DSK-08). Absent: `pinSubmit` answers `not_implemented`. */
  pin?: PinActions;
  /** "Install what's missing" (issue #480). Absent: its channels answer `not_implemented`. */
  ensure?: EnsureActions;
  /** The error-page actions (DSK-03); `host`, `supervisor`, `retry`... are wired by index.ts. */
  errorActions: ActionDeps;
  /** Reported when a settings patch was refused (never the values). */
  log?: Logger;
}

const asChild = (value: unknown): ChildWindowName | null => (typeof value === 'string' && CHILD_NAMES.includes(value) ? (value as ChildWindowName) : null);

/** `window.localaiDesktop`: every member of the spec's interface, the ones whose item has not landed answering `not_implemented`. */
export function createShellHandlers(deps: BridgeDeps): HandlerTable {
  const stub = (kind: HandlerKind = 'invoke'): HandlerSpec => ({ kind, origin: 'shell', run: () => NOT_IMPLEMENTED });
  const onLabPage = (event: SenderEvent): boolean => isShellPage(event.senderFrame?.url ?? '', 'lab', deps.policy.devOrigin);
  const onEnsurePage = (event: SenderEvent): boolean => isShellPage(event.senderFrame?.url ?? '', 'ensure', deps.policy.devOrigin);
  const onConnectionInfoPage = (event: SenderEvent): boolean => isShellPage(event.senderFrame?.url ?? '', 'connection-info', deps.policy.devOrigin);
  return {
    [CHANNELS.status]: { kind: 'invoke', origin: 'shell', run: () => deps.status() },
    [CHANNELS.lang]: { kind: 'sync', origin: 'shell', run: () => deps.lang() },
    [CHANNELS.openWindow]: {
      kind: 'send',
      origin: 'shell',
      run: (_event, which) => {
        const name = asChild(which);
        if (name) deps.openChild(name);
      },
    },
    [CHANNELS.showTab]: {
      kind: 'send',
      origin: 'shell',
      run: (_event, tab) => {
        if (typeof tab === 'string' && TABS.includes(tab)) deps.showTab(tab as Tab);
      },
    },
    [CHANNELS.setFullScreen]: {
      kind: 'send',
      origin: 'shell',
      run: (_event, flag) => {
        if (typeof flag === 'boolean') deps.setFullScreen?.(flag);
      },
    },
    [CHANNELS.closePortal]: { kind: 'send', origin: 'shell', run: () => deps.closePortal?.() },
    [CHANNELS.showMenu]: { kind: 'send', origin: 'shell', run: () => deps.showMenu() },
    [CHANNELS.settingsGet]: { kind: 'invoke', origin: 'shell', run: async () => projectSettings(await deps.settings.get()) },
    [CHANNELS.settingsSet]: {
      kind: 'invoke',
      origin: 'shell',
      run: async (_event, patch) => {
        const checked = validateSettingsPatch(patch);
        if (!checked.ok) {
          deps.log?.log('warn', 'app', 'a settings patch was refused', { reason: checked.reason });
          throw new Error(`settings: ${checked.reason}`);
        }
        return projectSettings(await deps.settings.update(checked.patch));
      },
    },
    [CHANNELS.errorAction]: {
      kind: 'invoke',
      origin: 'shell',
      // Only the error page itself (the bridge exists in every shell page).
      accept: (event) => isErrorPageUrl(event.senderFrame?.url ?? '', deps.policy.devOrigin),
      run: async (_event, payload): Promise<ActionResult> => {
        const request = parseActionRequest(payload);
        return request ? handleErrorAction(request, deps.errorActions) : { ok: false };
      },
    },
    [CHANNELS.pinSubmit]: {
      kind: 'invoke',
      origin: 'shell',
      accept: (event) => deps.pinWindowId() !== null && event.sender.id === deps.pinWindowId(),
      run: (event, pin) => (deps.pin ? deps.pin.submit(event.sender.id, pin) : NOT_IMPLEMENTED),
    },
    [CHANNELS.pinCancel]: {
      kind: 'send',
      origin: 'shell',
      accept: (event) => deps.pinWindowId() !== null && event.sender.id === deps.pinWindowId(),
      run: (event) => deps.pin?.cancel(event.sender.id),
    },
    [CHANNELS.setMode]: {
      kind: 'invoke',
      origin: 'shell',
      run: (_event, target) => {
        // A target the bridge does not know is refused here and never reaches the agent.
        if (typeof target !== 'string' || !SET_MODE_TARGETS.includes(target)) return { ok: false } satisfies ActionResult;
        return deps.setMode ? deps.setMode(target as NodeMode) : NOT_IMPLEMENTED;
      },
    },
    [CHANNELS.startAgent]: stub(), // DSK-05
    [CHANNELS.copyApiKey]: deps.keys ? { kind: 'invoke', origin: 'shell', run: () => deps.keys!.copy() } : stub(),
    // The key, its masked form and the rotation are for the connection-info page only (the bridge exists in every shell page).
    [CHANNELS.connectionInfo]: deps.keys ? { kind: 'invoke', origin: 'shell', accept: onConnectionInfoPage, run: () => deps.keys!.info() } : stub(),
    [CHANNELS.revealKey]: deps.keys ? { kind: 'invoke', origin: 'shell', accept: onConnectionInfoPage, run: () => deps.keys!.reveal() } : stub(),
    [CHANNELS.rotateKey]: deps.keys ? { kind: 'invoke', origin: 'shell', accept: onConnectionInfoPage, run: () => deps.keys!.rotate() } : stub(),
    // Issue #480: "Install what's missing", for the ensure window only. No argument is read except the names of the steps a
    // person approved, which the main process checks against the two the agent holds back.
    [CHANNELS.ensureView]: deps.ensure ? { kind: 'invoke', origin: 'shell', accept: onEnsurePage, run: () => deps.ensure!.view() } : stub(),
    [CHANNELS.ensureStart]: deps.ensure ? { kind: 'invoke', origin: 'shell', accept: onEnsurePage, run: () => deps.ensure!.start() } : stub(),
    [CHANNELS.ensureCancel]: deps.ensure ? { kind: 'invoke', origin: 'shell', accept: onEnsurePage, run: () => deps.ensure!.cancel() } : stub(),
    [CHANNELS.ensureConfirm]: deps.ensure ? { kind: 'invoke', origin: 'shell', accept: onEnsurePage, run: (_event, steps) => deps.ensure!.confirm(steps) } : stub(),
    // DSK-10: the lab link. No argument is read from the page: the code, the URL and the token live in the main process.
    [CHANNELS.cloudBeginLink]: {
      kind: 'invoke',
      origin: 'shell',
      run: async () => (deps.cloud ? deps.cloud.beginLink() : ({ ok: false, code: 'unavailable' } satisfies LinkRefusal)),
    },
    [CHANNELS.cloudOpenVerification]: { kind: 'send', origin: 'shell', run: () => deps.cloud?.openVerification() },
    [CHANNELS.cloudUnlink]: {
      kind: 'invoke',
      origin: 'shell',
      run: async () => {
        await deps.cloud?.unlink();
      },
    },
    // DSK-11: the Lab view only (the bridge exists in every shell page). The page names a method and an allowlisted path; the
    // main process adds the token, judges the path and the body (cloud-api.ts) and never forwards a URL or a header.
    [CHANNELS.cloudRequest]: {
      kind: 'invoke',
      origin: 'shell',
      accept: onLabPage,
      run: (_event, req) => (deps.lab ? deps.lab.request(req) : ({ ok: false, error: 'unavailable' } satisfies CloudResult)),
    },
    [CHANNELS.cloudCached]: { kind: 'invoke', origin: 'shell', accept: onLabPage, run: () => (deps.lab ? deps.lab.cached() : null) },
    [CHANNELS.cloudOpenNode]: { kind: 'send', origin: 'shell', accept: onLabPage, run: (_event, nodeId) => deps.lab?.openNode(nodeId) },
    [CHANNELS.cloudLinkState]: { kind: 'invoke', origin: 'shell', run: () => (deps.cloud ? deps.cloud.state() : deps.status().cloud) },
  };
}

/**
 * `window.localaiDesktopHost`: the agent UI may open the three windows, ask for the version, copy the key to the
 * clipboard (it never receives it) and read the connection info with the key masked.
 */
export function createHostHandlers(deps: Pick<BridgeDeps, 'version' | 'openChild' | 'keys'>): HandlerTable {
  return {
    [CHANNELS.hostVersion]: { kind: 'sync', origin: 'host', run: () => deps.version },
    [CHANNELS.hostOpenWindow]: {
      kind: 'send',
      origin: 'host',
      run: (_event, which) => {
        const name = asChild(which);
        if (name) deps.openChild(name);
      },
    },
    [CHANNELS.hostCopyApiKey]: { kind: 'invoke', origin: 'host', run: () => (deps.keys ? deps.keys.copy() : NOT_IMPLEMENTED) },
    [CHANNELS.hostConnectionInfo]: { kind: 'invoke', origin: 'host', run: () => (deps.keys ? deps.keys.info() : NOT_IMPLEMENTED) },
  };
}
