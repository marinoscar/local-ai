// The Lab view's brain (docs/specs/desktop.md 4.8): what to fetch, when to retry, what a command line says. No React and no
// DOM, so a unit test drives it with a fake bridge and a fake clock. The page renders `getState()`.
//
// Rules it keeps:
//   * Nothing runs while the Lab tab is hidden (no refresh, no retry, no command polling); showing the tab refreshes at once.
//   * The cloud is unreachable on a network error, a timeout, a 5xx, or when the agent's connectivity is `none`/`limited`.
//     Then the cached cards (main process, `lab-cache.json`) are shown with a banner, command buttons are off, and the retry
//     delay grows 10 s, 20 s, 40 s ... up to 5 min.
//   * A command is polled every 2 s until it settles.
//   * Issue #515: a node's workload switch and a music request are sent the same way, and a music request is polled every
//     2 s too. What a workload or a song is stays the cloud's and the node's business: the controller only carries the ask.
import {
  type CommandInfo,
  type ModelInfo,
  type MusicRequestInfo,
  type SongInfo,
  type WorkloadInfo,
  WORKLOAD_NAME_PATTERN,
  isSettled,
  projectCommand,
  projectLabNodes,
  projectModels,
  projectMusicRequest,
  projectSongs,
  projectWorkloads,
  readRefusal,
} from '../../shared/lab-data';
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

/** What the card's buttons ask for. `param` is a model id, a service name or a workload name. */
export type CommandKind = 'status.refresh' | 'model.load' | 'model.unload' | 'model.set_default' | 'ark.sync' | 'ark.verify' | 'service.restart' | 'workload.activate';

export interface CommandSpec {
  type: CommandKind;
  params: Record<string, unknown>;
  /** Model id or service name, for the line's text. */
  param?: string;
}

/** The sentence under a card after a command. `text` is already in the display language. */
export interface CommandLine {
  spec: CommandSpec;
  /**
   * `sending`: the POST is on its way. `refused`: the cloud (or the allowlist) did not take it. `sent`: the cloud took it
   * but answered no command to follow (nothing to poll; the cards are read again shortly).
   */
  status: 'sending' | 'refused' | 'sent' | CommandInfo['status'];
  id: string | null;
  text: string;
  /** The poll could not reach the cloud; it resumes when the cloud answers again. */
  stalled: boolean;
}

export type ModelsPanel = { open: boolean; state: 'loading' | 'ready' | 'error'; models: ModelInfo[] };
export type WorkloadsPanel = { open: boolean; state: 'loading' | 'ready' | 'error'; workloads: WorkloadInfo[] };
export type SongsPanel = { state: 'loading' | 'ready' | 'error'; songs: SongInfo[] };

/** What "Make music on this node" sends (contract §6, the text fields; the Lab sends no sample). */
export interface MusicInput {
  lyrics: string;
  tags: string;
  durationS: number;
  language: 'en' | 'es';
  variations: number;
  seed?: number;
}

/** The sentence under a card after a music request; like `CommandLine`, for the request instead of a command. */
export interface MusicLine {
  status: 'sending' | 'refused' | 'sent' | MusicRequestInfo['status'];
  id: string | null;
  text: string;
  stalled: boolean;
}

/** How many of a node's songs the Lab lists. */
export const SONGS_SHOWN = 5;

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
  workloads: Record<string, WorkloadsPanel>;
  music: Record<string, MusicLine>;
  songs: Record<string, SongsPanel>;
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
const MUSIC_REQUESTS = '/api/localai/music/requests';
const SONGS = '/api/localai/music/songs';
/** After a `sent` answer (nothing to poll), the cards are read again this much later. */
export const SENT_REFRESH_MS = 5_000;
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
  // Issue #515: the node's own answers to a workload switch or a song (contract §7), when the cloud passes them on.
  if (refusal.code === 'LAI-LLM-050') return pick(STRINGS.refusedWorkloadUnknown, lang);
  if (refusal.code === 'LAI-LLM-051') return pick(STRINGS.refusedWorkloadFit, lang);
  if (refusal.code === 'LAI-LLM-053') return pick(STRINGS.refusedWorkloadSurvival, lang);
  if (refusal.code === 'LAI-SVC-151') return pick(STRINGS.refusedMusicNotLoaded, lang);
  if (refusal.code === 'LAI-SVC-153') return pick(STRINGS.refusedMusicBusy, lang);
  if (result.status === 400 || result.status === 413 || result.status === 422) {
    if (refusal.code === 'LAI-SVC-154') return pick(STRINGS.refusedMusicInput, lang);
  }
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

/** The sentence for a music request that settled or is under way. */
export function musicStatusText(info: Pick<MusicRequestInfo, 'status' | 'errorCode' | 'errorMessage'>, lang: Lang): string {
  switch (info.status) {
    case 'queued':
      return pick(STRINGS.musicQueued, lang);
    case 'running':
      return pick(STRINGS.musicRunning, lang);
    case 'succeeded':
      return pick(STRINGS.musicDone, lang);
    case 'cancelled':
      return `${pick(STRINGS.musicCancelled, lang)} (LAI-SVC-158)`;
    default:
      return statusText(info, lang);
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
    this.#state = { lang, loaded: false, summary: null, nodes: [], dataAt: null, fromCache: false, unreachable: false, problem: null, connected: false, visible: false, user: null, commands: {}, models: {}, workloads: {}, music: {}, songs: {} };
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

  /** A music request of this node is on its way or not settled yet. */
  isMusicBusy(nodeId: string): boolean {
    const line = this.#state.music[nodeId];
    return line !== undefined && (line.status === 'sending' || line.status === 'queued' || line.status === 'running');
  }

  #setLine(nodeId: string, line: CommandLine): void {
    this.#set({ commands: { ...this.#state.commands, [nodeId]: line } });
  }

  async send(nodeId: string, spec: CommandSpec): Promise<void> {
    if (!this.canAct() || this.isBusy(nodeId)) return;
    const lang = this.#state.lang;
    let request: { method: 'POST'; path: string; body: unknown };
    if (spec.type === 'workload.activate') {
      // Its own route (contract §6); the name is checked here so a bad one never becomes a path.
      const name = spec.params['name'];
      if (typeof name !== 'string' || !WORKLOAD_NAME_PATTERN.test(name)) return;
      request = { method: 'POST', path: `${NODES}/${nodeId}/workloads/${name}/activate`, body: spec.params['force'] === true ? { force: true } : {} };
    } else {
      request = { method: 'POST', path: `${NODES}/${nodeId}/commands`, body: { type: spec.type, params: spec.params } };
    }
    this.#setLine(nodeId, { spec, status: 'sending', id: null, text: pick(STRINGS.sending, lang), stalled: false });
    const result = await this.#bridge.cloud.request(request);
    if (this.#stopped) return;
    const command = !isFailure(result) && result.ok ? projectCommand(result.body) : null;
    if (command === null && spec.type === 'workload.activate' && !isFailure(result) && result.ok) {
      // Taken, but nothing to follow: say so, and read the cards again shortly.
      this.#setLine(nodeId, { spec, status: 'sent', id: null, text: pick(STRINGS.sent, lang), stalled: false });
      this.#scheduleRefresh(SENT_REFRESH_MS);
      return;
    }
    if (command === null) {
      this.#setLine(nodeId, { spec, status: 'refused', id: null, text: refusalText(result, lang), stalled: false });
      if (isFailure(result) && result.error === 'unauthorized') this.#set({ problem: 'removed' });
      else if (isFailure(result) || result.status >= 500) void this.refresh();
      return;
    }
    this.#setLine(nodeId, { spec, status: command.status, id: command.id, text: statusText(command, lang), stalled: false });
    this.#ensurePolling();
  }

  /** Polls every active command and music request of every card, every 2 s, while the tab is shown. */
  #ensurePolling(): void {
    if (this.#pollTimer !== null || !this.#canRun()) return;
    const pending = (line: { id: string | null; stalled: boolean; status: string }): boolean => line.id !== null && !line.stalled && (line.status === 'queued' || line.status === 'running');
    const active = Object.values(this.#state.commands).some(pending) || Object.values(this.#state.music).some(pending);
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
        if (this.#state.models[nodeId]?.open || this.#state.workloads[nodeId]?.open) void this.#loadDetail(nodeId);
      }
    }
    for (const [nodeId, line] of Object.entries(this.#state.music)) {
      if (line.id === null || line.stalled || (line.status !== 'queued' && line.status !== 'running')) continue;
      const result = await this.#bridge.cloud.request({ method: 'GET', path: `${MUSIC_REQUESTS}/${line.id}` });
      if (this.#stopped) return;
      const info = !isFailure(result) && result.ok ? projectMusicRequest(result.body) : null;
      if (info === null) {
        this.#setMusic(nodeId, { ...line, stalled: true });
        void this.refresh();
        continue;
      }
      this.#setMusic(nodeId, { ...line, status: info.status, text: musicStatusText(info, lang) });
      if (isSettled(info.status) && this.#state.songs[nodeId] !== undefined) void this.loadSongs(nodeId);
    }
    // A command changes the card (a model loaded, a sync finished): read the cards again at once.
    if (settledSomething && this.#canRun()) {
      this.#failures = 0;
      void this.refresh();
    }
    this.#ensurePolling();
  }

  // -- models and workloads of one node (one read: `GET /api/localai/nodes/:nodeId`) ---------------------------------

  toggleModels(nodeId: string): void {
    const panel = this.#state.models[nodeId];
    if (panel?.open) {
      this.#set({ models: { ...this.#state.models, [nodeId]: { ...panel, open: false } } });
      return;
    }
    this.#set({ models: { ...this.#state.models, [nodeId]: { open: true, state: panel?.state ?? 'loading', models: panel?.models ?? [] } } });
    void this.#loadDetail(nodeId);
  }

  /** The node's workloads (issue #515): read from the node, switched with `workload.activate`. */
  toggleWorkloads(nodeId: string): void {
    const panel = this.#state.workloads[nodeId];
    if (panel?.open) {
      this.#set({ workloads: { ...this.#state.workloads, [nodeId]: { ...panel, open: false } } });
      return;
    }
    this.#set({ workloads: { ...this.#state.workloads, [nodeId]: { open: true, state: panel?.state ?? 'loading', workloads: panel?.workloads ?? [] } } });
    void this.#loadDetail(nodeId);
  }

  /** Switches the node to a workload; the node does the work and reports back through the command. */
  activateWorkload(nodeId: string, name: string): Promise<void> {
    return this.send(nodeId, { type: 'workload.activate', params: { name }, param: name });
  }

  async #loadDetail(nodeId: string): Promise<void> {
    const result = await this.#bridge.cloud.request({ method: 'GET', path: `${NODES}/${nodeId}` });
    if (this.#stopped) return;
    const body = !isFailure(result) && result.ok ? result.body : null;
    const models = body !== null ? projectModels(body) : null;
    const workloads = body !== null ? projectWorkloads(body) : null;
    const previousModels = this.#state.models[nodeId];
    const previousWorkloads = this.#state.workloads[nodeId];
    const patch: Partial<LabState> = {};
    if (previousModels) patch.models = { ...this.#state.models, [nodeId]: models === null ? { ...previousModels, state: 'error' } : { open: previousModels.open, state: 'ready', models } };
    if (previousWorkloads) {
      patch.workloads = { ...this.#state.workloads, [nodeId]: workloads === null ? { ...previousWorkloads, state: 'error' } : { open: previousWorkloads.open, state: 'ready', workloads } };
    }
    if (patch.models !== undefined || patch.workloads !== undefined) this.#set(patch);
  }

  // -- music on one node (issue #515) ---------------------------------------------------------------------------------

  #setMusic(nodeId: string, line: MusicLine): void {
    this.#set({ music: { ...this.#state.music, [nodeId]: line } });
  }

  /** "Make music on this node": `POST /api/localai/music/requests`, then the request is followed every 2 s. */
  async requestMusic(nodeId: string, input: MusicInput): Promise<void> {
    if (!this.canAct() || this.isMusicBusy(nodeId)) return;
    const lang = this.#state.lang;
    this.#setMusic(nodeId, { status: 'sending', id: null, text: pick(STRINGS.sending, lang), stalled: false });
    const body = {
      nodeId,
      lyrics: input.lyrics,
      tags: input.tags,
      duration_s: input.durationS,
      language: input.language,
      variations: input.variations,
      ...(input.seed !== undefined ? { seed: input.seed } : {}),
    };
    const result = await this.#bridge.cloud.request({ method: 'POST', path: MUSIC_REQUESTS, body });
    if (this.#stopped) return;
    const info = !isFailure(result) && result.ok ? projectMusicRequest(result.body) : null;
    if (info === null) {
      if (!isFailure(result) && result.ok) {
        this.#setMusic(nodeId, { status: 'sent', id: null, text: pick(STRINGS.musicSent, lang), stalled: false });
        return;
      }
      this.#setMusic(nodeId, { status: 'refused', id: null, text: refusalText(result, lang), stalled: false });
      if (isFailure(result) && result.error === 'unauthorized') this.#set({ problem: 'removed' });
      else if (isFailure(result) && result.error === 'refused') return;
      else if (isFailure(result) || result.status >= 500) void this.refresh();
      return;
    }
    this.#setMusic(nodeId, { status: info.status, id: info.id, text: musicStatusText(info, lang), stalled: false });
    this.#ensurePolling();
  }

  /** The node's latest songs (`GET /api/localai/music/songs`, filtered to the node here). */
  async loadSongs(nodeId: string): Promise<void> {
    const previous = this.#state.songs[nodeId];
    this.#set({ songs: { ...this.#state.songs, [nodeId]: { state: previous?.songs.length ? previous.state : 'loading', songs: previous?.songs ?? [] } } });
    const result = await this.#bridge.cloud.request({ method: 'GET', path: SONGS });
    if (this.#stopped) return;
    const songs = !isFailure(result) && result.ok ? projectSongs(result.body) : null;
    const current = this.#state.songs[nodeId] ?? { state: 'loading' as const, songs: [] };
    this.#set({
      songs: {
        ...this.#state.songs,
        [nodeId]: songs === null ? { ...current, state: 'error' } : { state: 'ready', songs: songs.filter((song) => song.nodeId === null || song.nodeId === nodeId).slice(0, SONGS_SHOWN) },
      },
    });
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

// ---------------------------------------------------------------------------------------------------------------------
// The music form (issue #515)
// ---------------------------------------------------------------------------------------------------------------------

/** What the form fields hold, as typed. */
export interface MusicForm {
  lyrics: string;
  tags: string;
  duration: string;
  language: 'en' | 'es';
  variations: string;
}

export const MUSIC_FORM_DEFAULTS: MusicForm = { lyrics: '', tags: '', duration: '60', language: 'en', variations: '1' };

/** Why the form cannot be sent yet: a key of STRINGS. */
export type MusicFormProblem = 'musicNeedsText' | 'musicTooLong' | 'musicTagsTooLong';

const clampInt = (raw: string, min: number, max: number, fallback: number): number => {
  const value = Number.parseInt(raw, 10);
  return Number.isFinite(value) ? Math.min(max, Math.max(min, value)) : fallback;
};

/**
 * The form -> what is sent. Only what keeps a person from a refusal they could not understand: some text, the two
 * size limits; the numbers are brought into range instead of refused (contract §3: 10-240 s, 1-4 versions).
 */
export function musicInputFrom(form: MusicForm): { ok: true; input: MusicInput } | { ok: false; problem: MusicFormProblem } {
  if (form.lyrics.trim() === '' && form.tags.trim() === '') return { ok: false, problem: 'musicNeedsText' };
  if (new TextEncoder().encode(form.lyrics).length > 8 * 1024) return { ok: false, problem: 'musicTooLong' };
  if (new TextEncoder().encode(form.tags).length > 512) return { ok: false, problem: 'musicTagsTooLong' };
  return {
    ok: true,
    input: { lyrics: form.lyrics, tags: form.tags.trim(), durationS: clampInt(form.duration, 10, 240, 60), language: form.language, variations: clampInt(form.variations, 1, 4, 1) },
  };
}

/** The node is known to be on another workload than `music` (the hint in the dialog). Unknown: false. */
export function musicWorkloadInactive(panel: WorkloadsPanel | undefined): boolean {
  if (!panel || panel.state !== 'ready') return false;
  const music = panel.workloads.find((workload) => workload.name === 'music');
  return music !== undefined && !music.active;
}
