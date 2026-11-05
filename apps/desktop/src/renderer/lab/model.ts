// The Lab view's brain (docs/specs/desktop.md 4.8): what to fetch, when to retry, what a command line says. No React and no
// DOM, so a unit test drives it with a fake bridge and a fake clock. The page renders `getState()`.
//
// Rules it keeps:
//   * Nothing runs while the Lab tab is hidden (no refresh, no retry, no command polling); showing the tab refreshes at once.
//   * The cloud is unreachable on a network error, a timeout, a 5xx, or when the agent's connectivity is `none`/`limited`.
//     Then the cached cards (main process, `lab-cache.json`) are shown with a banner, command buttons are off, and the retry
//     delay grows 10 s, 20 s, 40 s ... up to 5 min.
//   * A command is polled every 2 s until it settles.
import { type CommandInfo, type ModelInfo, isSettled, projectCommand, projectLabNodes, projectModels, readRefusal } from '../../shared/lab-data';
import type { CloudResult, DesktopStatus, Lang, LocalaiDesktop } from '../../shared/types';
import type { LabSummaryDto, NodeCardDto } from '@localai/ui/lab/types';
import { STRINGS, fill, pick } from './strings';

export const REFRESH_MS = 15_000;
export const COMMAND_POLL_MS = 2_000;
export const RETRY_FIRST_MS = 10_000;
export const RETRY_MAX_MS = 5 * 60_000;

/** The delay before retry number `failures` (0 = the first retry): 10 s, 20 s, 40 s ... capped at 5 min. */
export function retryDelayMs(failures: number): number {
  return Math.min(RETRY_MAX_MS, RETRY_FIRST_MS * 2 ** Math.max(0, failures));
}

/** The agent's connectivity says there is no usable internet: do not even try (desktop.md 4.8). */
export const connectivityBlocks = (connectivity: string | null): boolean => connectivity === 'none' || connectivity === 'limited';

/** `14:05` for a time today, `Tue 2 Nov 14:05` for an older one. */
export function clockLabel(iso: string, lang: Lang, now: Date = new Date(), timeZone?: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return '';
  const zone = timeZone !== undefined ? { timeZone } : {};
  const time = new Intl.DateTimeFormat(lang, { hour: '2-digit', minute: '2-digit', hour12: false, ...zone }).format(at);
  const day = (date: Date): string => new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit', ...zone }).format(date);
  if (day(at) === day(now)) return time;
  return `${new Intl.DateTimeFormat(lang, { weekday: 'short', day: 'numeric', month: 'short', ...zone }).format(at)} ${time}`;
}

// ---------------------------------------------------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------------------------------------------------

/** What the card's buttons ask for. `param` is a model id or a service name. */
export type CommandKind = 'status.refresh' | 'model.load' | 'model.unload' | 'model.set_default' | 'ark.sync' | 'ark.verify' | 'service.restart';

export interface CommandSpec {
  type: CommandKind;
  params: Record<string, unknown>;
  /** Model id or service name, for the line's text. */
  param?: string;
}

/** The sentence under a card after a command. `text` is already in the display language. */
export interface CommandLine {
  spec: CommandSpec;
  /** `sending`: the POST is on its way. `refused`: the cloud (or the allowlist) did not take it. */
  status: 'sending' | 'refused' | CommandInfo['status'];
  id: string | null;
  text: string;
  /** The poll could not reach the cloud; it resumes when the cloud answers again. */
  stalled: boolean;
}

export type ModelsPanel = { open: boolean; state: 'loading' | 'ready' | 'error'; models: ModelInfo[] };

export type Problem = 'forbidden' | 'removed' | 'unexpected' | null;

export interface LabState {
  lang: Lang;
  /** The first load (or cache read) has finished. */
  loaded: boolean;
  summary: LabSummaryDto | null;
  nodes: NodeCardDto[];
  /** When the cards on screen were fetched (ISO). */
  dataAt: string | null;
  /** The cards come from `lab-cache.json`. */
  fromCache: boolean;
  unreachable: boolean;
  problem: Problem;
  connected: boolean;
  visible: boolean;
  user: string | null;
  commands: Record<string, CommandLine>;
  models: Record<string, ModelsPanel>;
}

/** Everything the controller needs from the outside: the preload bridge, a clock and timers. */
export interface LabBridge {
  status(): Promise<DesktopStatus>;
  onStatus(cb: (status: DesktopStatus) => void): () => void;
  cloud: Pick<LocalaiDesktop['cloud'], 'request' | 'cached' | 'openNode'>;
}

export interface Clock {
  now(): number;
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

export const realClock: Clock = {
  now: () => Date.now(),
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

const NODES = '/api/localai/nodes';
const isFailure = (result: CloudResult): result is Extract<CloudResult, { error: unknown }> => 'error' in result;

/** Words for a refused command: the cloud's doctor code and reason, in the display language. */
export function refusalText(result: CloudResult, lang: Lang): string {
  if (isFailure(result)) {
    if (result.error === 'unauthorized') return pick(STRINGS.removed, lang);
    return pick(result.error === 'refused' ? STRINGS.refusedGeneric : STRINGS.refusedUnreachable, lang);
  }
  const refusal = readRefusal(result.body);
  if (result.status === 403) return pick(STRINGS.refusedForbidden, lang);
  if (result.status === 409 && refusal.code === 'LAI-CLD-035') return pick(STRINGS.refusedNotEnrolled, lang);
  if (result.status === 422 && refusal.code === 'LAI-CLD-034') return pick(refusal.mode === 'survival' ? STRINGS.refusedSurvival : STRINGS.refusedOffline, lang);
  if (result.status === 422 && refusal.reason === 'model_not_on_node') return pick(STRINGS.refusedModel, lang);
  if (result.status >= 500) return pick(STRINGS.refusedUnreachable, lang);
  return pick(STRINGS.refusedGeneric, lang);
}

/** The sentence for a command that settled or is under way. */
export function statusText(info: Pick<CommandInfo, 'status' | 'errorCode' | 'errorMessage'>, lang: Lang): string {
  switch (info.status) {
    case 'queued':
      return pick(STRINGS.waiting, lang);
    case 'running':
      return pick(STRINGS.running, lang);
    case 'succeeded':
      return pick(STRINGS.done, lang);
    case 'expired':
      return pick(STRINGS.expired, lang);
    case 'cancelled':
      return pick(STRINGS.cancelled, lang);
    case 'failed': {
      const detail = [info.errorMessage, info.errorCode !== null ? `(${info.errorCode})` : null].filter((part): part is string => part !== null && part !== '').join(' ');
      return detail === '' ? pick(STRINGS.failed, lang) : `${pick(STRINGS.failed, lang)}: ${detail}`;
    }
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// The controller
// ---------------------------------------------------------------------------------------------------------------------

export class LabController {
  readonly #bridge: LabBridge;
  readonly #clock: Clock;
  #state: LabState;
  readonly #listeners = new Set<() => void>();
  #unsubscribe: (() => void) | null = null;
  #connectivity: string | null = null;
  #documentVisible = true;
  #tabVisible = false;
  #failures = 0;
  #refreshTimer: unknown = null;
  #pollTimer: unknown = null;
  #refreshing = false;
  #stopped = false;
  /** The tab is shown, the window is visible and connected mode is active: the only time anything runs. */
  #active = false;

  constructor(bridge: LabBridge, lang: Lang, clock: Clock = realClock) {
    this.#bridge = bridge;
    this.#clock = clock;
    this.#state = { lang, loaded: false, summary: null, nodes: [], dataAt: null, fromCache: false, unreachable: false, problem: null, connected: false, visible: false, user: null, commands: {}, models: {} };
  }

  // -- store ---------------------------------------------------------------------------------------------------------

  readonly subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  };

  readonly getState = (): LabState => this.#state;

  #set(patch: Partial<LabState>): void {
    this.#state = { ...this.#state, ...patch };
    for (const listener of this.#listeners) listener();
  }

  // -- life cycle ----------------------------------------------------------------------------------------------------

  /** Reads the status once, then follows the pushes. */
  async start(): Promise<void> {
    this.#unsubscribe = this.#bridge.onStatus((status) => this.#onStatus(status));
    this.#onStatus(await this.#bridge.status());
  }

  stop(): void {
    this.#stopped = true;
    this.#unsubscribe?.();
    this.#unsubscribe = null;
    this.#clearTimers();
  }

  /** `visibilitychange` of the page: a minimised or hidden window counts as a hidden tab. */
  setDocumentVisible(visible: boolean): void {
    if (this.#documentVisible === visible) return;
    this.#documentVisible = visible;
    this.#reconsider();
  }

  #onStatus(status: DesktopStatus): void {
    const wasBlocked = connectivityBlocks(this.#connectivity);
    this.#connectivity = status.connectivity;
    this.#tabVisible = status.tab === 'lab';
    const wasConnected = this.#state.connected;
    this.#state = { ...this.#state, connected: status.connected, user: status.cloud.user ?? null };
    // Connected mode came back: whatever stopped the Lab (a 401, a 403) is worth another look.
    if (status.connected && !wasConnected) this.#state = { ...this.#state, problem: null };
    // Internet came back while the lab was unreachable: do not wait for the backoff.
    const recovered = wasBlocked && !connectivityBlocks(status.connectivity) && this.#state.unreachable;
    this.#reconsider(recovered);
  }

  /** The tab or the window was shown or hidden, or the status changed. `retryNow`: leave the backoff. */
  #reconsider(retryNow = false): void {
    const active = this.#tabVisible && this.#documentVisible && this.#state.connected;
    const was = this.#active;
    this.#active = active;
    this.#set({ visible: this.#tabVisible && this.#documentVisible });
    if (!active) {
      this.#clearTimers();
      return;
    }
    if (!was || retryNow) {
      this.#failures = 0;
      void this.refresh();
      this.#ensurePolling();
    }
  }

  #clearTimers(): void {
    if (this.#refreshTimer !== null) this.#clock.clear(this.#refreshTimer);
    if (this.#pollTimer !== null) this.#clock.clear(this.#pollTimer);
    this.#refreshTimer = null;
    this.#pollTimer = null;
  }

  #canRun(): boolean {
    return !this.#stopped && this.#state.visible && this.#state.connected;
  }

  #scheduleRefresh(ms: number): void {
    if (this.#refreshTimer !== null) this.#clock.clear(this.#refreshTimer);
    this.#refreshTimer = null;
    if (!this.#canRun()) return;
    this.#refreshTimer = this.#clock.set(() => {
      this.#refreshTimer = null;
      void this.refresh();
    }, ms);
  }

  // -- the node list -------------------------------------------------------------------------------------------------

  /** Loads the cards now (the "Try now" button, a shown tab, a timer). Does nothing while hidden or already loading. */
  async refresh(): Promise<void> {
    if (!this.#canRun() || this.#refreshing) return;
    this.#refreshing = true;
    try {
      if (connectivityBlocks(this.#connectivity)) {
        await this.#fail();
        return;
      }
      const result = await this.#bridge.cloud.request({ method: 'GET', path: NODES });
      if (this.#stopped) return;
      if (isFailure(result)) {
        if (result.error === 'unauthorized') {
          this.#set({ problem: 'removed', loaded: true, unreachable: false });
          return;
        }
        // 'offline' / 'aborted': the node left `online`; the main process hides this tab next. 'refused' cannot happen for this path.
        await this.#fail();
        return;
      }
      if (result.status === 403) {
        this.#set({ problem: 'forbidden', loaded: true, unreachable: false });
        this.#scheduleRefresh(REFRESH_MS);
        return;
      }
      const data = result.ok ? projectLabNodes(result.body) : null;
      if (data === null) {
        if (result.ok) this.#set({ problem: 'unexpected' });
        await this.#fail();
        return;
      }
      this.#failures = 0;
      this.#set({ ...data, dataAt: new Date(this.#clock.now()).toISOString(), fromCache: false, unreachable: false, problem: null, loaded: true });
      this.#scheduleRefresh(REFRESH_MS);
      this.#ensurePolling();
    } finally {
      this.#refreshing = false;
    }
  }

  /** Unreachable: show the cache (when there are no cards yet), and retry later with a growing delay. */
  async #fail(): Promise<void> {
    let patch: Partial<LabState> = { unreachable: true, loaded: true };
    if (this.#state.nodes.length === 0 || this.#state.fromCache) {
      const cached = await this.#bridge.cloud.cached().catch(() => null);
      if (cached) patch = { ...patch, summary: cached.summary, nodes: cached.nodes, dataAt: cached.savedAt, fromCache: true };
    }
    if (this.#stopped) return;
    this.#set(patch);
    this.#scheduleRefresh(retryDelayMs(this.#failures));
    this.#failures += 1;
  }

  // -- commands ------------------------------------------------------------------------------------------------------

  /** Command buttons work only against live data from a reachable cloud. */
  canAct(): boolean {
    return this.#state.connected && this.#state.visible && !this.#state.unreachable && !this.#state.fromCache && this.#state.problem === null;
  }

  isBusy(nodeId: string): boolean {
    const line = this.#state.commands[nodeId];
    return line !== undefined && (line.status === 'sending' || line.status === 'queued' || line.status === 'running');
  }

  #setLine(nodeId: string, line: CommandLine): void {
    this.#set({ commands: { ...this.#state.commands, [nodeId]: line } });
  }

  async send(nodeId: string, spec: CommandSpec): Promise<void> {
    if (!this.canAct() || this.isBusy(nodeId)) return;
    const lang = this.#state.lang;
    this.#setLine(nodeId, { spec, status: 'sending', id: null, text: pick(STRINGS.sending, lang), stalled: false });
    const result = await this.#bridge.cloud.request({ method: 'POST', path: `${NODES}/${nodeId}/commands`, body: { type: spec.type, params: spec.params } });
    if (this.#stopped) return;
    const command = !isFailure(result) && result.ok ? projectCommand(result.body) : null;
    if (command === null) {
      this.#setLine(nodeId, { spec, status: 'refused', id: null, text: refusalText(result, lang), stalled: false });
      if (isFailure(result) && result.error === 'unauthorized') this.#set({ problem: 'removed' });
      else if (isFailure(result) || result.status >= 500) void this.refresh();
      return;
    }
    this.#setLine(nodeId, { spec, status: command.status, id: command.id, text: statusText(command, lang), stalled: false });
    this.#ensurePolling();
  }

  /** Polls every active command of every card, every 2 s, while the tab is shown. */
  #ensurePolling(): void {
    if (this.#pollTimer !== null || !this.#canRun()) return;
    const active = Object.values(this.#state.commands).some((line) => line.id !== null && !line.stalled && (line.status === 'queued' || line.status === 'running'));
    if (!active) return;
    this.#pollTimer = this.#clock.set(() => {
      this.#pollTimer = null;
      void this.#pollOnce();
    }, COMMAND_POLL_MS);
  }

  async #pollOnce(): Promise<void> {
    if (!this.#canRun()) return;
    const lang = this.#state.lang;
    let settledSomething = false;
    for (const [nodeId, line] of Object.entries(this.#state.commands)) {
      if (line.id === null || line.stalled || (line.status !== 'queued' && line.status !== 'running')) continue;
      const result = await this.#bridge.cloud.request({ method: 'GET', path: `/api/localai/commands/${line.id}` });
      if (this.#stopped) return;
      const info = !isFailure(result) && result.ok ? projectCommand(result.body) : null;
      if (info === null) {
        // Keep the last words, stop asking, and let the node list find out what is wrong with the cloud.
        this.#setLine(nodeId, { ...line, stalled: true });
        void this.refresh();
        continue;
      }
      this.#setLine(nodeId, { ...line, status: info.status, text: statusText(info, lang) });
      if (isSettled(info.status)) {
        settledSomething = true;
        if (this.#state.models[nodeId]?.open) void this.#loadModels(nodeId);
      }
    }
    // A command changes the card (a model loaded, a sync finished): read the cards again at once.
    if (settledSomething && this.#canRun()) {
      this.#failures = 0;
      void this.refresh();
    }
    this.#ensurePolling();
  }

  // -- models of one node --------------------------------------------------------------------------------------------

  toggleModels(nodeId: string): void {
    const panel = this.#state.models[nodeId];
    if (panel?.open) {
      this.#set({ models: { ...this.#state.models, [nodeId]: { ...panel, open: false } } });
      return;
    }
    this.#set({ models: { ...this.#state.models, [nodeId]: { open: true, state: panel?.state ?? 'loading', models: panel?.models ?? [] } } });
    void this.#loadModels(nodeId);
  }

  async #loadModels(nodeId: string): Promise<void> {
    const result = await this.#bridge.cloud.request({ method: 'GET', path: `${NODES}/${nodeId}` });
    if (this.#stopped) return;
    const models = !isFailure(result) && result.ok ? projectModels(result.body) : null;
    const previous = this.#state.models[nodeId];
    if (!previous) return;
    this.#set({ models: { ...this.#state.models, [nodeId]: models === null ? { ...previous, state: 'error' } : { open: previous.open, state: 'ready', models } } });
  }

  openNode(nodeId: string): void {
    this.#bridge.cloud.openNode(nodeId);
  }

  // -- text helpers the page uses -------------------------------------------------------------------------------------

  bannerText(now: Date = new Date(), timeZone?: string): string | null {
    const { unreachable, dataAt, lang } = this.#state;
    if (!unreachable) return null;
    const time = dataAt !== null ? clockLabel(dataAt, lang, now, timeZone) : '';
    return time === '' ? pick(STRINGS.unreachableNoData, lang) : fill(pick(STRINGS.unreachable, lang), { time });
  }
}
