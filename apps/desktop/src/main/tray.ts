// Tray icon, status colour, menu, mode switch and the "no tray" fallback (docs/specs/desktop.md 4.3, 5).
//
// The module has no hard Electron dependency. Everything stateful sits behind a small interface that the tests replace
// with a fake: the tray itself (`TrayPort`), native dialogs (`Dialogs`), the windows (`WindowControl`, which DSK-04's
// window manager implements), the PIN prompt (`PinPrompt`, which DSK-08 implements), timers and the clock. The real
// Electron objects are handed to the `createElectron*` adapters at the bottom, from index.ts.
//
// Sections: 1 status colour (pure), 2 menu model (pure), 3 mode switch, 4 the controller, 5 tray availability and hide
// to tray, 6 Electron adapters.
import { execFile } from 'node:child_process';
import { join } from 'node:path';
import { AgentTokenError, type AgentClient, type AgentEvent, type AgentStatus, type FeedMessage } from './agent-client';
import {
  type Bilingual,
  type Lang,
  type TrayColour,
  DIALOG_TEXT,
  ERROR_COPY,
  TRAY_TEXT,
  TRAY_TOOLTIPS,
  isDskCode,
  pick,
} from './i18n';
import { type Logger, NULL_LOGGER } from './log';
import { type NotificationPort, type Notifier, NOTIFY_WINDOW_MS } from './notify';
import { readSettings, updateSettings } from './settings';

export const MODULE = 'tray' as const;

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

// ---------------------------------------------------------------------------------------------------------------------
// 1. Status colour
// ---------------------------------------------------------------------------------------------------------------------

/**
 * How the app stands with the agent:
 * - `starting`: nothing known yet, or the event stream dropped and the agent is coming back (grey);
 * - `up`: the stream is open and `/status` was read;
 * - `down`: the agent does not answer (red, and the "Start agent" item);
 * - `blocked`: the agent answers but this app cannot use it (token file, refused login): red, no "Start agent".
 */
export type AgentLink = 'starting' | 'up' | 'down' | 'blocked';

export interface TrayStatusInput {
  link: AgentLink;
  /** `online`, `offline` or `survival`; undefined before the first `/status`. */
  mode: string | undefined;
  /** A model failed to load and has not loaded since. */
  modelFailed: boolean;
}

export interface AlertLike {
  code?: string;
  severity?: string;
}

/** Connectivity `none` for this long while online turns the light amber (the agent's own suggestion rule uses the same 30 min). */
export const NO_INTERNET_MS = 30 * 60 * 1000;

function severityRank(alerts: readonly AlertLike[]): 'critical' | 'warning' | null {
  let result: 'critical' | 'warning' | null = null;
  for (const alert of alerts) {
    if (alert.severity === 'critical') return 'critical';
    // `error` is not in the table of desktop.md 4.3; it needs attention like a warning does.
    if (alert.severity === 'warning' || alert.severity === 'error') result = 'warning';
  }
  return result;
}

/**
 * The colour of the tray icon (desktop.md 4.3). Precedence: grey (starting) > red (agent down or a critical alert) >
 * orange (survival) > amber (warning alert, a failed model, or no connectivity for 30 minutes while online) > blue
 * (offline) > green (online). `noneSince` is `status.connectivity.none_since`: the agent's own clock, not a local timer.
 */
export function trayColour(
  status: TrayStatusInput | null,
  alerts: readonly AlertLike[],
  connectivity: string | undefined,
  noneSince: string | null | undefined,
  now: number,
): TrayColour {
  if (status === null || status.link === 'starting') return 'grey';
  if (status.link === 'down' || status.link === 'blocked') return 'red';
  const worst = severityRank(alerts);
  if (worst === 'critical') return 'red';
  if (status.mode === 'survival') return 'orange';
  const since = typeof noneSince === 'string' ? Date.parse(noneSince) : Number.NaN;
  const noInternetLong = status.mode === 'online' && connectivity === 'none' && Number.isFinite(since) && now - since >= NO_INTERNET_MS;
  if (worst === 'warning' || status.modelFailed || noInternetLong) return 'amber';
  if (status.mode === 'offline') return 'blue';
  if (status.mode === 'online') return 'green';
  // A mode this version does not know: do not claim "ready".
  return 'grey';
}

/** The parts of `GET /status` the tray reads, with the types checked. */
export interface ReadStatus {
  mode: string | undefined;
  alerts: AlertLike[];
  connectivity: string | undefined;
  noneSince: string | null;
  /** The model to show in the menu header: the first loaded one, else the default. */
  model: string | null;
}

export function readStatus(status: AgentStatus | null): ReadStatus {
  const raw = status ?? {};
  const mode = isObject(raw['mode']) && typeof raw['mode']['value'] === 'string' ? raw['mode']['value'] : undefined;
  const alerts = Array.isArray(raw['alerts'])
    ? (raw['alerts'] as unknown[]).filter(isObject).map((a) => ({
        ...(typeof a['code'] === 'string' ? { code: a['code'] } : {}),
        ...(typeof a['severity'] === 'string' ? { severity: a['severity'] } : {}),
      }))
    : [];
  const connectivity = isObject(raw['connectivity']) ? raw['connectivity'] : {};
  const models = isObject(raw['models']) ? raw['models'] : {};
  const loaded = Array.isArray(models['loaded']) ? (models['loaded'] as unknown[]).find((m): m is string => typeof m === 'string') : undefined;
  const fallback = typeof models['default'] === 'string' ? models['default'] : undefined;
  return {
    mode,
    alerts,
    connectivity: typeof connectivity['value'] === 'string' ? connectivity['value'] : undefined,
    noneSince: typeof connectivity['none_since'] === 'string' ? connectivity['none_since'] : null,
    model: loaded ?? fallback ?? null,
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// 2. Menu model
// ---------------------------------------------------------------------------------------------------------------------

export type ModeTarget = 'online' | 'offline' | 'survival';
export type WindowName = 'chat' | 'library' | 'maps';

export type MenuAction =
  | 'open'
  | 'window:chat'
  | 'window:library'
  | 'window:maps'
  | 'mode:online'
  | 'mode:offline'
  | 'survival'
  | 'copy-key'
  | 'connection-info'
  | 'lab'
  | 'start-agent'
  | 'autostart'
  | 'quit';

export type MenuItemSpec =
  | { type: 'separator' }
  | { type: 'label'; label: string }
  | { type: 'action'; action: MenuAction; label: string; enabled: boolean }
  | { type: 'radio'; action: MenuAction; label: string; checked: boolean; enabled: boolean }
  | { type: 'check'; action: MenuAction; label: string; checked: boolean }
  | { type: 'submenu'; label: string; items: MenuItemSpec[] };

/** Which optional items exist: an item whose owner (a later item of epic E8) has not landed yet is left out. */
export interface MenuFeatures {
  /** Chat, Library and Maps windows (DSK-04). */
  windows: boolean;
  /** DSK-08. */
  copyKey: boolean;
  connectionInfo: boolean;
  /** DSK-11, and only while the app is connected to the lab. */
  lab: boolean;
  /** DSK-05: the agent can be started from here. */
  startAgent: boolean;
  /** DSK-07: `null` leaves the item out, otherwise the check mark. */
  autostart: boolean | null;
  /** DSK-08: a PIN prompt exists, so survival can be left from the tray. */
  pin: boolean;
}

export const NO_FEATURES: MenuFeatures = { windows: false, copyKey: false, connectionInfo: false, lab: false, startAgent: false, autostart: null, pin: false };

export interface MenuModel {
  colour: TrayColour;
  link: AgentLink;
  mode: string | undefined;
  /** The model name of the header, shown only while the agent is up. */
  model: string | null;
  features: MenuFeatures;
}

/** The menu of desktop.md 4.3 for one state. Pure; the labels are in `lang`. */
export function buildTrayMenu(model: MenuModel, lang: Lang): MenuItemSpec[] {
  const t = (text: Bilingual): string => pick(text, lang);
  const f = model.features;
  const up = model.link === 'up';

  const header = `● ${t(TRAY_TOOLTIPS[model.colour])}${up && model.model !== null ? ` · ${model.model}` : ''}`;
  const groups: MenuItemSpec[][] = [[{ type: 'label', label: header }]];

  const open: MenuItemSpec[] = [{ type: 'action', action: 'open', label: t(TRAY_TEXT.open), enabled: true }];
  if (f.windows) {
    open.push(
      { type: 'action', action: 'window:chat', label: t(TRAY_TEXT.chat), enabled: up },
      { type: 'action', action: 'window:library', label: t(TRAY_TEXT.library), enabled: up },
      { type: 'action', action: 'window:maps', label: t(TRAY_TEXT.maps), enabled: up },
    );
  }
  groups.push(open);

  // Leaving survival needs the PIN. Without the PIN prompt (DSK-08) the tray cannot do it and says where to.
  const inSurvival = model.mode === 'survival';
  const canSwitch = up && (!inSurvival || f.pin);
  const modeItems: MenuItemSpec[] = [
    { type: 'radio', action: 'mode:online', label: t(TRAY_TEXT.online), checked: model.mode === 'online', enabled: canSwitch },
    { type: 'radio', action: 'mode:offline', label: t(TRAY_TEXT.offline), checked: model.mode === 'offline', enabled: canSwitch },
  ];
  if (inSurvival && !f.pin) modeItems.push({ type: 'separator' }, { type: 'label', label: t(TRAY_TEXT.leaveSurvival) });
  groups.push([
    { type: 'submenu', label: t(TRAY_TEXT.mode), items: modeItems },
    // Always enabled, never asks for a PIN: anyone can switch to the offline setup in one click.
    { type: 'action', action: 'survival', label: t(TRAY_TEXT.survival), enabled: true },
  ]);

  const tools: MenuItemSpec[] = [];
  if (f.copyKey) tools.push({ type: 'action', action: 'copy-key', label: t(TRAY_TEXT.copyKey), enabled: up });
  if (f.connectionInfo) tools.push({ type: 'action', action: 'connection-info', label: t(TRAY_TEXT.connectionInfo), enabled: up });
  if (f.lab) tools.push({ type: 'action', action: 'lab', label: t(TRAY_TEXT.lab), enabled: true });
  if (f.startAgent && model.link === 'down') tools.push({ type: 'action', action: 'start-agent', label: t(TRAY_TEXT.startAgent), enabled: true });
  if (tools.length > 0) groups.push(tools);

  const last: MenuItemSpec[] = [];
  if (f.autostart !== null) last.push({ type: 'check', action: 'autostart', label: t(TRAY_TEXT.startAtLogin), checked: f.autostart });
  last.push({ type: 'action', action: 'quit', label: t(TRAY_TEXT.quit), enabled: true });
  groups.push(last);

  return groups.flatMap((group, index) => (index === 0 ? group : [{ type: 'separator' } as const, ...group]));
}

// ---------------------------------------------------------------------------------------------------------------------
// 3. Mode switch
// ---------------------------------------------------------------------------------------------------------------------

export const PIN_GRANT_HEADER = 'X-LocalAI-Pin-Grant';

/** The IPC channel behind `window.localaiDesktop.setMode(target)` (same string in src/preload/shell.ts). */
export const SET_MODE_CHANNEL = 'localai:set-mode' as const;

export function parseModeTarget(value: unknown): ModeTarget | null {
  return value === 'online' || value === 'offline' || value === 'survival' ? value : null;
}

/** A shell page (`localai://app/...`, or the Vite dev origin of an unpackaged build) may call the mode bridge; nothing else may. */
export function isShellPageUrl(url: string, devOrigin?: string): boolean {
  try {
    const parsed = new URL(url);
    if (parsed.protocol === 'localai:') return parsed.hostname === 'app';
    return devOrigin !== undefined && parsed.origin === new URL(devOrigin).origin;
  } catch {
    return false;
  }
}

export type ModeOutcome =
  | { ok: true; already: boolean }
  | { ok: false; code: string; message?: string; fix?: string; pinRequired?: boolean; cancelled?: boolean };

export interface ModeInfo {
  mode: string | undefined;
  /** `/mode.suggest_survival`: online, and connectivity `none` for more than 30 minutes. */
  suggestSurvival: boolean;
}

export interface ModeApi {
  /** `POST /api/v1/mode {target, wait:false}`, or `POST /api/v1/mode/survival {}` without any credential. */
  request(target: ModeTarget, options?: { pinGrant?: string }): Promise<ModeOutcome>;
  /** `GET /api/v1/mode`, or null when it cannot be read. */
  read(): Promise<ModeInfo | null>;
}

/** A failure to reach or use the agent at all, as the doctor code the user sees (desktop.md 4.1, 5). */
export function codeForRequestError(error: unknown): string {
  if (error instanceof AgentTokenError) return error.reason === 'permission-denied' ? 'LAI-DSK-001' : 'LAI-DSK-003';
  return 'LAI-DSK-004';
}

export function createModeApi(client: Pick<AgentClient, 'request'>): ModeApi {
  return {
    async request(target, options = {}) {
      const survival = target === 'survival';
      let response: Response;
      try {
        response = await client.request(survival ? '/api/v1/mode/survival' : '/api/v1/mode', {
          method: 'POST',
          json: survival ? {} : { target, wait: false },
          // Survival needs no login, so it works with an unreadable token file. Everything else carries the bearer.
          ...(survival ? { anonymous: true } : {}),
          ...(options.pinGrant !== undefined ? { headers: { [PIN_GRANT_HEADER]: options.pinGrant } } : {}),
        });
      } catch (error) {
        return { ok: false, code: codeForRequestError(error) };
      }
      let body: unknown = null;
      try {
        body = await response.json();
      } catch {
        // an empty or damaged body: the status alone decides
      }
      if (response.status === 200 || response.status === 202) return { ok: true, already: isObject(body) && body['already'] === true };
      const envelope = isObject(body) && isObject(body['error']) ? body['error'] : {};
      const details = isObject(envelope['details']) ? envelope['details'] : {};
      return {
        ok: false,
        code: typeof envelope['code'] === 'string' ? envelope['code'] : `HTTP ${response.status}`,
        ...(typeof envelope['message'] === 'string' ? { message: envelope['message'] } : {}),
        ...(typeof envelope['fix'] === 'string' ? { fix: envelope['fix'] } : {}),
        ...(response.status === 403 && envelope['code'] === 'LAI-SEC-002' && details['pin_required'] === true ? { pinRequired: true } : {}),
      };
    },
    async read() {
      try {
        const response = await client.request('/api/v1/mode');
        if (!response.ok) return null;
        const body: unknown = await response.json();
        if (!isObject(body)) return null;
        return { mode: typeof body['mode'] === 'string' ? body['mode'] : undefined, suggestSurvival: body['suggest_survival'] === true };
      } catch {
        return null;
      }
    },
  };
}

/** DSK-08's native PIN window. The tray only asks for a grant; it never sees the PIN. */
export interface PinPrompt {
  /** Resolves with the grant, or without one when the user cancelled or the agent refused (`code` is the agent's, if any). */
  requestGrant(purpose: 'leave-survival'): Promise<{ ok: true; grant: string } | { ok: false; code?: string; cancelled?: boolean }>;
}

export interface ModeControllerDeps {
  api: ModeApi;
  /** Absent until DSK-08: the tray then cannot leave survival and says so. */
  pin?: PinPrompt;
  /** The mode as last seen, to decide whether the PIN is needed before asking the agent. */
  currentMode(): string | undefined;
  log?: Logger;
}

/**
 * The one place a mode change is requested: the tray menu and the `setMode` bridge of the shell pages both call it.
 * Online and offline carry the bearer token; survival carries nothing. Leaving survival needs a PIN grant, which is
 * used for this one request and dropped.
 */
export class ModeController {
  readonly #deps: ModeControllerDeps;
  readonly #log: Logger;
  #prompting = false;

  constructor(deps: ModeControllerDeps) {
    this.#deps = deps;
    this.#log = deps.log ?? NULL_LOGGER;
  }

  /** Whether survival can be left from here. */
  get canLeaveSurvival(): boolean {
    return this.#deps.pin !== undefined;
  }

  async setMode(target: ModeTarget): Promise<ModeOutcome> {
    if (target === 'survival') return this.#send(target);
    let grant: string | undefined;
    if (this.#deps.currentMode() === 'survival') {
      // Known to need the PIN: do not make the agent audit a refusal.
      const asked = await this.#grant();
      if (!asked.ok) return asked.outcome;
      grant = asked.grant;
    }
    const first = await this.#send(target, grant);
    if (first.ok || !first.pinRequired || grant !== undefined) return first;
    // The mode we knew was stale: the agent says the PIN is needed after all.
    const asked = await this.#grant();
    if (!asked.ok) return asked.outcome;
    return this.#send(target, asked.grant);
  }

  async #send(target: ModeTarget, grant?: string): Promise<ModeOutcome> {
    const outcome = await this.#deps.api.request(target, grant === undefined ? {} : { pinGrant: grant });
    this.#log.log(outcome.ok ? 'info' : 'warn', 'tray', outcome.ok ? `mode change to ${target} accepted` : `mode change to ${target} refused`, outcome.ok ? { already: outcome.already } : { code: outcome.code });
    return outcome;
  }

  async #grant(): Promise<{ ok: true; grant: string } | { ok: false; outcome: ModeOutcome }> {
    const pin = this.#deps.pin;
    if (pin === undefined) return { ok: false, outcome: { ok: false, code: 'LAI-SEC-002' } };
    if (this.#prompting) return { ok: false, outcome: { ok: false, code: 'LAI-SEC-002', cancelled: true } };
    this.#prompting = true;
    try {
      const result = await pin.requestGrant('leave-survival');
      if (result.ok) return { ok: true, grant: result.grant };
      return { ok: false, outcome: { ok: false, code: result.code ?? 'LAI-SEC-002', ...(result.cancelled === true ? { cancelled: true } : {}) } };
    } finally {
      this.#prompting = false;
    }
  }
}

export interface DialogText {
  title: string;
  message: string;
  detail?: string;
}

/** The native dialog for a refused mode change: the agent's code and fix (desktop.md 4.3, implementation note 5). Pure. */
export function describeModeFailure(failure: Extract<ModeOutcome, { ok: false }>, lang: Lang): DialogText {
  const title = pick(DIALOG_TEXT.modeFailedTitle, lang);
  if (isDskCode(failure.code)) {
    const copy = ERROR_COPY[failure.code];
    return { title, message: pick(copy.message, lang), detail: `${pick(DIALOG_TEXT.fixLabel, lang)}: ${pick(copy.fix, lang)}${copy.command !== undefined ? ` ${copy.command}` : ''}\n\n${failure.code}` };
  }
  return {
    title,
    message: failure.message ?? pick(DIALOG_TEXT.modeFailedFallback, lang),
    detail: `${failure.fix !== undefined ? `${pick(DIALOG_TEXT.fixLabel, lang)}: ${failure.fix}\n\n` : ''}${failure.code}`,
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// 4. The controller
// ---------------------------------------------------------------------------------------------------------------------

/** What the tray shows: the Electron adapter turns it into an icon, a tooltip and a context menu. */
export interface TrayView {
  colour: TrayColour;
  tooltip: string;
  menu: MenuItemSpec[];
}

export interface TrayPort {
  update(view: TrayView): void;
  destroy(): void;
}

export interface Dialogs {
  confirm(options: { title: string; message: string; confirmLabel: string; cancelLabel: string }): Promise<boolean>;
  alert(options: DialogText & { okLabel: string }): Promise<void>;
}

/** What the tray needs from the window manager. DSK-04's implements it; until then index.ts adapts its one window. */
export interface WindowControl {
  /** Show, restore and focus the main window. */
  showMain(): void;
  /** Chat, Library or Maps (DSK-04). Absent until then: those items are left out of the menu. */
  openWindow?(which: WindowName): void;
}

export interface Timers {
  /** Runs `fn` once after `ms`; returns the function that cancels it. */
  after(ms: number, fn: () => void): () => void;
}

export const realTimers: Timers = {
  after(ms, fn) {
    const handle = setTimeout(fn, ms);
    handle.unref();
    return () => clearTimeout(handle);
  },
};

/** The actions behind the optional menu items. Each belongs to a later item of epic E8; the item exists once its action does. */
export interface TrayActions {
  quit(): void;
  copyApiKey?(): void | Promise<void>;
  showConnectionInfo?(): void | Promise<void>;
  openLab?(): void | Promise<void>;
  /** The app is linked to the lab (the item shows only then). */
  labConnected?(): boolean;
  /** DSK-05: starts the agent; true when it was started. The result is the caller's to show. */
  startAgent?(): void | Promise<void>;
  autostart?: { get(): boolean; set(next: boolean): void | Promise<void> };
}

export interface TrayControllerDeps {
  port: TrayPort;
  lang(): Lang;
  /** Milliseconds, any epoch. `Date.now` in the app. */
  clock(): number;
  timers?: Timers;
  notifier: Pick<Notifier, 'notifyEvent' | 'suggestSurvival'>;
  modes: Pick<ModeController, 'setMode' | 'canLeaveSurvival'>;
  /** `GET /health`, one try: is the agent there, and is it still starting. */
  probe(): Promise<'up' | 'starting' | 'down'>;
  /** Re-reads `/status` (the feed emits a `status` message when it works). */
  refreshStatus(): Promise<void>;
  readMode(): Promise<ModeInfo | null>;
  windows: WindowControl;
  dialogs: Dialogs;
  actions: TrayActions;
  log?: Logger;
  /** How often a connected tray re-reads `/status` (the connectivity clock has no event). Default 60 s. */
  tickMs?: number;
  /** How often a tray that lost the agent asks `/health`. Default 3 s (the manual test wants red within 5 s). */
  probeMs?: number;
}

export class TrayController {
  readonly #d: TrayControllerDeps;
  readonly #timers: Timers;
  readonly #log: Logger;
  #link: AgentLink = 'starting';
  #stream = 'connecting';
  #startupBlocked = false;
  #status: AgentStatus | null = null;
  readonly #alerts = new Map<string, AlertLike>();
  readonly #failedModels = new Set<string>();
  #modeSeen: string | undefined;
  #suggested = false;
  #lastView = '';
  #disposed = false;
  #probing = false;
  #refreshing = false;
  #refreshAgain = false;
  #cancelTick: (() => void) | null = null;
  #cancelProbe: (() => void) | null = null;

  constructor(deps: TrayControllerDeps) {
    this.#d = deps;
    this.#timers = deps.timers ?? realTimers;
    this.#log = deps.log ?? NULL_LOGGER;
  }

  /** The mode as last seen (for `ModeController.currentMode`). */
  get mode(): string | undefined {
    return this.#modeSeen;
  }

  get link(): AgentLink {
    return this.#link;
  }

  /** Shows the first (grey) state and starts the clock that keeps time-based colours honest. */
  start(): void {
    this.#render();
    this.#scheduleTick();
  }

  dispose(): void {
    this.#disposed = true;
    this.#cancelTick?.();
    this.#cancelProbe?.();
    this.#d.port.destroy();
  }

  /** The start-up sequence ended on an error page: red, and look for the agent to come back. */
  startupFailed(kind: 'down' | 'blocked'): void {
    this.#startupBlocked = kind === 'blocked';
    this.#setLink(kind);
    this.#ensureProbe();
  }

  /** The feed's messages: connection changes, `/status` reads and agent events. */
  handle(message: FeedMessage): void {
    if (this.#disposed) return;
    if (message.kind === 'status') this.#applyStatus(message.status);
    else if (message.kind === 'connection') this.#onConnection(message.state);
    else this.#onEvent(message.event);
  }

  // -- link ------------------------------------------------------------------------------------------------------------

  #setLink(link: AgentLink): void {
    if (link !== 'up') this.#failedModels.clear();
    this.#link = link;
    this.#render();
  }

  #onConnection(state: string): void {
    this.#stream = state;
    switch (state) {
      case 'connected':
        this.#startupBlocked = false;
        this.#cancelProbe?.();
        this.#cancelProbe = null;
        this.#setLink('up');
        void this.#requestRefresh();
        break;
      case 'reconnecting':
      case 'shutdown':
      case 'unauthorized':
        // The agent restarting looks like this too: grey first, then `/health` says whether it is gone.
        this.#setLink(state === 'unauthorized' && this.#link === 'down' ? 'down' : 'starting');
        this.#ensureProbe();
        break;
      default:
        break;
    }
  }

  #ensureProbe(): void {
    if (this.#probing || this.#disposed) return;
    this.#probing = true;
    void this.#probeLoop();
  }

  async #probeLoop(): Promise<void> {
    try {
      while (!this.#disposed && this.#stream !== 'connected') {
        const result = await this.#d.probe().catch((): 'down' => 'down');
        if (this.#disposed || this.#stream === 'connected') break;
        if (result === 'down') this.#setLink('down');
        else if (result === 'starting') this.#setLink('starting');
        else this.#setLink(this.#stream === 'unauthorized' || this.#startupBlocked ? 'blocked' : 'starting');
        await new Promise<void>((resolve) => {
          this.#cancelProbe = this.#timers.after(this.#d.probeMs ?? 3000, resolve);
        });
      }
    } finally {
      this.#probing = false;
    }
  }

  // -- status and events -------------------------------------------------------------------------------------------------

  #applyStatus(status: AgentStatus): void {
    this.#status = status;
    const read = readStatus(status);
    this.#modeSeen = read.mode ?? this.#modeSeen;
    this.#alerts.clear();
    for (const alert of read.alerts) this.#alerts.set(alert.code ?? `anonymous-${this.#alerts.size}`, alert);
    // A status that arrives is proof that the agent answers.
    this.#startupBlocked = false;
    this.#link = 'up';
    this.#render();
    if (read.mode === 'online' && read.connectivity === 'none') void this.#checkSuggestion();
    else this.#suggested = false;
  }

  /** Coalesced: events arrive in bursts, and `/status` is read once per burst. */
  async #requestRefresh(): Promise<void> {
    if (this.#refreshing) {
      this.#refreshAgain = true;
      return;
    }
    this.#refreshing = true;
    try {
      do {
        this.#refreshAgain = false;
        await this.#d.refreshStatus().catch(() => undefined);
      } while (this.#refreshAgain && !this.#disposed);
    } finally {
      this.#refreshing = false;
    }
  }

  #onEvent(event: AgentEvent): void {
    const data = isObject(event.data) ? event.data : {};
    const model = typeof data['model'] === 'string' ? data['model'] : undefined;
    // After a reconnect the agent replays what was missed; an old event is history, not news.
    const stale = event.at !== undefined && this.#d.clock() - Date.parse(event.at) > NOTIFY_WINDOW_MS;
    const announce = (): void => {
      if (!stale) this.#d.notifier.notifyEvent(event);
    };
    switch (event.type) {
      case 'mode.changed':
        if (typeof data['to'] === 'string') this.#modeSeen = data['to'];
        announce();
        this.#render();
        void this.#requestRefresh();
        break;
      case 'model.failed':
        if (model !== undefined) this.#failedModels.add(model);
        announce();
        this.#render();
        break;
      case 'model.loaded':
      case 'model.unloaded':
        if (model !== undefined) this.#failedModels.delete(model);
        this.#render();
        void this.#requestRefresh();
        break;
      case 'model.default_changed':
        void this.#requestRefresh();
        break;
      case 'sync.done':
      case 'drive.mounted':
      case 'drive.unmounted':
        announce();
        break;
      case 'alert.raised': {
        const code = typeof data['code'] === 'string' ? data['code'] : undefined;
        if (code !== undefined) this.#alerts.set(code, { code, ...(typeof data['severity'] === 'string' ? { severity: data['severity'] } : {}) });
        announce();
        this.#render();
        // LAI-NET-011 is the agent saying "no internet for 30 minutes": ask whether it suggests survival now.
        if (code === 'LAI-NET-011') void this.#checkSuggestion();
        break;
      }
      case 'alert.cleared':
        if (typeof data['code'] === 'string') this.#alerts.delete(data['code']);
        this.#render();
        break;
      default:
        break;
    }
  }

  /** `/mode.suggest_survival` turning true is a notification, once per rise. Never changes the mode. */
  async #checkSuggestion(): Promise<void> {
    const info = await this.#d.readMode().catch(() => null);
    if (info === null || this.#disposed) return;
    if (info.suggestSurvival && !this.#suggested) this.#d.notifier.suggestSurvival();
    this.#suggested = info.suggestSurvival;
  }

  #scheduleTick(): void {
    if (this.#disposed) return;
    this.#cancelTick = this.#timers.after(this.#d.tickMs ?? 60_000, () => {
      if (this.#link === 'up') void this.#requestRefresh();
      // Colours that depend on the passing of time (30 minutes without internet) change with no event.
      this.#render();
      this.#scheduleTick();
    });
  }

  // -- view --------------------------------------------------------------------------------------------------------------

  #features(): MenuFeatures {
    const a = this.#d.actions;
    return {
      windows: this.#d.windows.openWindow !== undefined,
      copyKey: a.copyApiKey !== undefined,
      connectionInfo: a.showConnectionInfo !== undefined,
      lab: a.openLab !== undefined && a.labConnected?.() === true,
      startAgent: a.startAgent !== undefined,
      autostart: a.autostart ? a.autostart.get() : null,
      pin: this.#d.modes.canLeaveSurvival,
    };
  }

  /** The view for the current state. Public for the tests; the controller pushes it to the port itself. */
  view(): TrayView {
    const read = readStatus(this.#status);
    const mode = this.#modeSeen ?? read.mode;
    const input: TrayStatusInput | null = this.#status === null && this.#link === 'up' ? null : { link: this.#link, mode, modelFailed: this.#failedModels.size > 0 };
    const colour = trayColour(input, [...this.#alerts.values()], read.connectivity, read.noneSince, this.#d.clock());
    const lang = this.#d.lang();
    return {
      colour,
      tooltip: pick(TRAY_TOOLTIPS[colour], lang),
      menu: buildTrayMenu({ colour, link: this.#link, mode, model: read.model, features: this.#features() }, lang),
    };
  }

  #render(): void {
    if (this.#disposed) return;
    const view = this.view();
    const key = JSON.stringify(view);
    // Rebuilding a menu while it is open closes it: only push a real change.
    if (key === this.#lastView) return;
    this.#lastView = key;
    this.#d.port.update(view);
  }

  // -- actions -----------------------------------------------------------------------------------------------------------

  /** A click on a menu item. Never throws: a failure is shown in a native dialog or logged. */
  async invoke(action: MenuAction): Promise<void> {
    try {
      await this.#run(action);
    } catch (error) {
      this.#log.log('error', 'tray', `menu action ${action} failed: ${(error as Error).message}`);
    }
  }

  /** The tray icon itself was clicked: bring the window forward (red means "click for help", and the window has it). */
  activate(): void {
    this.#d.windows.showMain();
  }

  async #run(action: MenuAction): Promise<void> {
    const a = this.#d.actions;
    switch (action) {
      case 'open':
        return this.#d.windows.showMain();
      case 'window:chat':
        return this.#d.windows.openWindow?.('chat');
      case 'window:library':
        return this.#d.windows.openWindow?.('library');
      case 'window:maps':
        return this.#d.windows.openWindow?.('maps');
      case 'mode:online':
        return this.#changeMode('online');
      case 'mode:offline':
        return this.#changeMode('offline');
      case 'survival': {
        const lang = this.#d.lang();
        const confirmed = await this.#d.dialogs.confirm({
          title: pick(DIALOG_TEXT.survivalTitle, lang),
          message: pick(DIALOG_TEXT.survivalQuestion, lang),
          confirmLabel: pick(DIALOG_TEXT.survivalYes, lang),
          cancelLabel: pick(DIALOG_TEXT.cancel, lang),
        });
        if (confirmed) await this.#changeMode('survival');
        return;
      }
      case 'copy-key':
        return void (await a.copyApiKey?.());
      case 'connection-info':
        return void (await a.showConnectionInfo?.());
      case 'lab':
        return void (await a.openLab?.());
      case 'start-agent':
        return void (await a.startAgent?.());
      case 'autostart':
        if (a.autostart) await a.autostart.set(!a.autostart.get());
        this.#render();
        return;
      case 'quit':
        return a.quit();
    }
  }

  async #changeMode(target: ModeTarget): Promise<void> {
    const outcome = await this.#d.modes.setMode(target);
    if (outcome.ok || outcome.cancelled === true) return;
    const lang = this.#d.lang();
    await this.#d.dialogs.alert({ ...describeModeFailure(outcome, lang), okLabel: pick(DIALOG_TEXT.ok, lang) });
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// 5. Tray availability and hide to tray
// ---------------------------------------------------------------------------------------------------------------------

export type TrayAvailability = 'available' | 'unavailable' | 'unknown';

/** Runs a fixed command. `missing`: the program is not installed. `failed`: it ran and said no (or could not reach the bus). */
export type CommandRunner = (file: string, args: readonly string[]) => Promise<{ status: 'ok'; stdout: string } | { status: 'missing' } | { status: 'failed' }>;

/** Names a StatusNotifier host answers on (GNOME's AppIndicator extension and KDE both own the first). */
export const WATCHER_NAMES = ['org.kde.StatusNotifierWatcher', 'org.freedesktop.StatusNotifierWatcher'] as const;

/** `busctl call ... NameHasOwner` prints `b true`; `gdbus call` prints `(true,)`. */
export function parseNameHasOwner(stdout: string): boolean | null {
  const text = stdout.trim();
  if (/^b\s+true$/.test(text) || /^\(true,?\)$/.test(text)) return true;
  if (/^b\s+false$/.test(text) || /^\(false,?\)$/.test(text)) return false;
  return null;
}

const DBUS_TOOLS: ReadonlyArray<(name: string) => readonly [string, string[]]> = [
  (name) => ['/usr/bin/busctl', ['--user', '--no-pager', 'call', 'org.freedesktop.DBus', '/org/freedesktop/DBus', 'org.freedesktop.DBus', 'NameHasOwner', 's', name]],
  (name) => ['/usr/bin/gdbus', ['call', '--session', '--dest', 'org.freedesktop.DBus', '--object-path', '/org/freedesktop/DBus', '--method', 'org.freedesktop.DBus.NameHasOwner', name]],
];

/**
 * Is a StatusNotifier watcher on the session bus (desktop.md 4.3, implementation note 3)? `unknown` only when no D-Bus
 * client program exists to ask; the caller then trusts Electron. Fixed arguments, no shell.
 */
export async function detectTray(run: CommandRunner = defaultRunner): Promise<TrayAvailability> {
  for (const build of DBUS_TOOLS) {
    let ran = false;
    for (const name of WATCHER_NAMES) {
      const [file, args] = build(name);
      const result = await run(file, args);
      if (result.status === 'missing') break;
      ran = true;
      if (result.status === 'ok' && parseNameHasOwner(result.stdout) === true) return 'available';
    }
    if (ran) return 'unavailable';
  }
  return 'unknown';
}

const defaultRunner: CommandRunner = (file, args) =>
  new Promise((resolve) => {
    execFile(file, [...args], { timeout: 2000, encoding: 'utf8', windowsHide: true }, (error, stdout) => {
      if (!error) return resolve({ status: 'ok', stdout });
      const code = (error as NodeJS.ErrnoException).code;
      resolve(code === 'ENOENT' ? { status: 'missing' } : { status: 'failed' });
    });
  });

/** `unknown` counts as available: the tray is tried, and a desktop without any D-Bus tool is not a desktop we know. */
export const trayUsable = (availability: TrayAvailability): boolean => availability !== 'unavailable';

export interface HideableWindow {
  on(event: 'close', listener: (event: { preventDefault(): void }) => void): unknown;
  hide(): void;
}

/**
 * Closing the main window hides it to the tray; Quit (or any app quit) really closes it. With no tray the listener is
 * not installed, so the window closes and `window-all-closed` quits the app (desktop.md 4.3).
 */
export function installHideToTray(win: HideableWindow, state: { trayActive(): boolean; quitting(): boolean }): void {
  win.on('close', (event) => {
    if (state.trayActive() && !state.quitting()) {
      event.preventDefault();
      win.hide();
    }
  });
}

export const TRAY_NOTICE_KEY = 'LAI-DSK-013' as const;

/** Remembers in settings.json (`notices`) that the no-tray notice was shown, so it is shown once ever. */
export function trayNoticeStore(dir: string): { shown(): Promise<boolean>; markShown(): Promise<void> } {
  return {
    async shown() {
      const notices = (await readSettings(dir))['notices'];
      return isObject(notices) && notices[TRAY_NOTICE_KEY] === true;
    },
    async markShown() {
      await updateSettings(dir, { notices: { [TRAY_NOTICE_KEY]: true } });
    },
  };
}

/** LAI-DSK-013, once. Returns true when it was shown now. */
export async function showTrayNoticeOnce(deps: { store: { shown(): Promise<boolean>; markShown(): Promise<void> }; notify(): boolean }): Promise<boolean> {
  if (await deps.store.shown().catch(() => false)) return false;
  deps.notify();
  await deps.store.markShown().catch(() => undefined);
  return true;
}

// ---------------------------------------------------------------------------------------------------------------------
// 6. Electron adapters (the real objects are passed in by index.ts; the tests pass fakes)
// ---------------------------------------------------------------------------------------------------------------------

export interface ElectronTrayLike {
  setImage(image: unknown): void;
  setToolTip(text: string): void;
  setContextMenu(menu: unknown): void;
  on(event: 'click', listener: () => void): unknown;
  destroy(): void;
}

export interface ElectronTrayApi {
  Tray: new (image: unknown) => ElectronTrayLike;
  Menu: { buildFromTemplate(template: unknown[]): unknown };
  nativeImage: { createFromPath(path: string): unknown };
}

/** `tray-<colour>.png` (22 px) and `tray-<colour>@2x.png` (44 px) sit side by side; Electron picks the @2x file by itself. */
export const trayIconPath = (iconDir: string, colour: TrayColour): string => join(iconDir, `tray-${colour}.png`);

/** The menu spec as an Electron menu template. */
export function toMenuTemplate(items: readonly MenuItemSpec[], onAction: (action: MenuAction) => void): unknown[] {
  return items.map((item) => {
    switch (item.type) {
      case 'separator':
        return { type: 'separator' };
      case 'label':
        return { label: item.label, enabled: false };
      case 'action':
        return { label: item.label, enabled: item.enabled, click: () => onAction(item.action) };
      case 'radio':
        return { type: 'radio', label: item.label, checked: item.checked, enabled: item.enabled, click: () => onAction(item.action) };
      case 'check':
        return { type: 'checkbox', label: item.label, checked: item.checked, click: () => onAction(item.action) };
      case 'submenu':
        return { label: item.label, submenu: toMenuTemplate(item.items, onAction) };
    }
  });
}

export function createElectronTrayPort(api: ElectronTrayApi, options: { iconDir: string; onAction(action: MenuAction): void; onActivate(): void }): TrayPort {
  let tray: ElectronTrayLike | null = null;
  let colour: TrayColour | null = null;
  return {
    update(view) {
      if (tray === null) {
        tray = new api.Tray(api.nativeImage.createFromPath(trayIconPath(options.iconDir, view.colour)));
        tray.on('click', options.onActivate);
        colour = view.colour;
      } else if (colour !== view.colour) {
        tray.setImage(api.nativeImage.createFromPath(trayIconPath(options.iconDir, view.colour)));
        colour = view.colour;
      }
      tray.setToolTip(view.tooltip);
      tray.setContextMenu(api.Menu.buildFromTemplate(toMenuTemplate(view.menu, options.onAction)));
    },
    destroy() {
      tray?.destroy();
      tray = null;
    },
  };
}

export interface ElectronDialogApi {
  showMessageBox(options: { type: string; title: string; message: string; detail?: string; buttons: string[]; defaultId: number; cancelId: number; noLink: boolean }): Promise<{ response: number }>;
}

export function createElectronDialogs(dialog: ElectronDialogApi): Dialogs {
  return {
    async confirm(o) {
      const { response } = await dialog.showMessageBox({ type: 'question', title: o.title, message: o.message, buttons: [o.cancelLabel, o.confirmLabel], defaultId: 1, cancelId: 0, noLink: true });
      return response === 1;
    },
    async alert(o) {
      await dialog.showMessageBox({ type: 'error', title: o.title, message: o.message, ...(o.detail !== undefined ? { detail: o.detail } : {}), buttons: [o.okLabel], defaultId: 0, cancelId: 0, noLink: true });
    },
  };
}

export interface ElectronNotificationApi {
  Notification: {
    new (options: { title: string; body: string }): { show(): void; on(event: 'click' | 'close' | 'failed', listener: () => void): unknown };
    isSupported(): boolean;
  };
}

export function createElectronNotificationPort(api: ElectronNotificationApi): NotificationPort {
  // Electron drops a notification that is garbage collected before the user clicks it: keep each one until it closes.
  const live = new Set<unknown>();
  return {
    supported: () => api.Notification.isSupported(),
    show(notification, onClick) {
      const shown = new api.Notification({ title: notification.title, body: notification.body });
      live.add(shown);
      const release = (): void => void live.delete(shown);
      shown.on('click', () => {
        release();
        onClick();
      });
      shown.on('close', release);
      shown.on('failed', release);
      shown.show();
    },
  };
}
