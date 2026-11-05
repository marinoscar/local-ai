// Opt-in updates (docs/specs/desktop.md sections 3.6, 4.10 and 4.11): OFF by default, and while off this module makes no
// request and does not even load `electron-updater`.
//
//   * `updateGate` is the table: the "Check for updates" setting AND the node's mode decide whether an update may run.
//     Offline and Survival are off whatever the setting says (4.9); an unknown mode (the agent has not answered) is off.
//   * `UpdateController` owns the 6 h timer, the egress allowance (`setEgress`, which index.ts wires to
//     `setEgressState({ updates })`), the status the menu shows and LAI-DSK-016. Turning it off stops the timer, closes
//     the egress allowance first, cancels what is in flight and stops installing on quit.
//   * The release is found here, not by electron-updater's GitHub provider: that provider reads the tag `v<version>` of
//     the repository's latest release, and this repository also carries `node-v*` and `catalog-*` releases. So the
//     controller reads the public release feed, picks the newest `desktop-v*` tag, and points electron-updater's
//     generic provider at that release's download folder; electron-updater then reads `latest-linux.yml`, downloads
//     the AppImage or the deb and verifies its SHA-512.
//   * `electron-updater` is loaded by `loadElectronUpdater` only (a dynamic import, so the bundler writes it to its own
//     chunk that nothing reads before the first check) and its traffic goes through the guarded `updater` session.
//
// No Electron import: the updater, the clock, the timers, the fetch and the egress switch are injected, so the whole
// module is unit-tested, including "zero requests when off over 24 hours".
import type { AppUpdater } from 'electron-updater';
import type { NodeMode } from '../shared/types';
import { type Lang, UPDATE_TEXT, fill, pick } from './i18n';
import { type Logger, NULL_LOGGER } from './log';

export const MODULE = 'updates' as const;

/** The doctor code of a failed check or download (doctor-codes.md). */
export const DOCTOR_CODE = 'LAI-DSK-016' as const;
/** desktop.md 4.10: "every 6 h". */
export const CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000;
/** After start-up or a switch back to Online the first check waits, so it never competes with start-up. Ticking the box checks at once. */
export const FIRST_CHECK_DELAY_MS = 30 * 1000;
export const TAG_PREFIX = 'desktop-v';
/** The public release feed and the folder a release's assets download from (hosts: UPDATE_HOSTS in egress-guard.ts). */
export const RELEASES_FEED_URL = 'https://github.com/marinoscar/local-ai/releases.atom';
export const RELEASE_FOLDER_URL = 'https://github.com/marinoscar/local-ai/releases/download';

// ---------------------------------------------------------------------------------------------------------------------
// The gate: setting x mode x connected
// ---------------------------------------------------------------------------------------------------------------------

export interface UpdateGate {
  /** `settings.updates.checkForUpdates`. */
  setting: boolean;
  /** The node's mode from the agent's `/status`; null until it was read. */
  mode: NodeMode | null;
  /** "Connected mode is active" (4.9). It implies mode `online`, so it never opens the gate by itself. */
  connected: boolean;
}

export type GateReason = 'allowed' | 'setting-off' | 'offline' | 'survival' | 'mode-unknown';

export interface GateDecision {
  allowed: boolean;
  reason: GateReason;
}

/**
 * May an update check or download run now? Only with the setting ON and the node ONLINE (connected mode is a part of
 * online). Offline and Survival are off even when the box is ticked, and so is a mode nobody has read yet: when the
 * mode is not known, the rule that no connection leaves a node that may be offline wins.
 */
export function updateGate(gate: UpdateGate): GateDecision {
  if (!gate.setting) return { allowed: false, reason: 'setting-off' };
  if (gate.mode === 'offline') return { allowed: false, reason: 'offline' };
  if (gate.mode === 'survival') return { allowed: false, reason: 'survival' };
  if (gate.mode !== 'online') return { allowed: false, reason: 'mode-unknown' };
  return { allowed: true, reason: 'allowed' };
}

// ---------------------------------------------------------------------------------------------------------------------
// Finding the newest desktop release
// ---------------------------------------------------------------------------------------------------------------------

interface Semver {
  major: number;
  minor: number;
  patch: number;
  pre: string[];
}

const SEMVER = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/;

export function parseSemver(text: string): Semver | null {
  const m = SEMVER.exec(text);
  if (!m) return null;
  return { major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]), pre: m[4] === undefined ? [] : m[4].split('.') };
}

/** SemVer 2.0 precedence: -1, 0 or 1. A version with a pre-release part is lower than the same version without. */
export function compareSemver(a: Semver, b: Semver): number {
  for (const key of ['major', 'minor', 'patch'] as const) {
    if (a[key] !== b[key]) return a[key] < b[key] ? -1 : 1;
  }
  if (a.pre.length === 0 || b.pre.length === 0) return a.pre.length === b.pre.length ? 0 : a.pre.length === 0 ? 1 : -1;
  for (let i = 0; i < Math.max(a.pre.length, b.pre.length); i++) {
    const x = a.pre[i];
    const y = b.pre[i];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    if (x === y) continue;
    const xn = /^\d+$/.test(x);
    const yn = /^\d+$/.test(y);
    if (xn && yn) return Number(x) < Number(y) ? -1 : 1;
    if (xn !== yn) return xn ? -1 : 1;
    return x < y ? -1 : 1;
  }
  return 0;
}

export interface DesktopRelease {
  tag: string;
  version: string;
}

/** Every `releases/tag/<tag>` link of a GitHub release feed (Atom), in feed order. Anything else in the text is ignored. */
export function tagsFromFeed(feed: string): string[] {
  const tags: string[] = [];
  for (const m of feed.matchAll(/href="[^"]*\/releases\/tag\/([^"/?#]+)"/g)) {
    try {
      tags.push(decodeURIComponent(m[1] ?? ''));
    } catch {
      // a malformed escape is not a tag we know
    }
  }
  return tags;
}

/**
 * The newest `desktop-v<semver>` release newer than `currentVersion`, or null. A pre-release (`-rc.1`) is considered
 * only when the running version is itself a pre-release, so a person on a stable build is never moved to a release
 * candidate.
 */
export function newestDesktopRelease(tags: readonly string[], currentVersion: string): DesktopRelease | null {
  const current = parseSemver(currentVersion);
  if (!current) return null;
  let best: { release: DesktopRelease; semver: Semver } | null = null;
  for (const tag of tags) {
    if (!tag.startsWith(TAG_PREFIX)) continue;
    const version = tag.slice(TAG_PREFIX.length);
    const semver = parseSemver(version);
    if (!semver) continue;
    if (semver.pre.length > 0 && current.pre.length === 0) continue;
    if (compareSemver(semver, current) <= 0) continue;
    if (best === null || compareSemver(semver, best.semver) > 0) best = { release: { tag, version }, semver };
  }
  return best?.release ?? null;
}

// ---------------------------------------------------------------------------------------------------------------------
// The updater port, and the adapter for electron-updater
// ---------------------------------------------------------------------------------------------------------------------

/** What the controller needs from an updater. The one real implementation is `createElectronUpdaterPort`. */
export interface UpdaterPort {
  /** Points the updater at one release's download folder (the folder that holds `latest-linux.yml`). */
  setReleaseFolder(url: string): void;
  /** Reads that folder's `latest-linux.yml`; when the version is newer, downloads it (SHA-512 checked). Resolves after the download. */
  checkAndDownload(): Promise<{ available: boolean; version: string }>;
  /** Cancels a download in progress. */
  cancel(): void;
  /** Whether a downloaded update installs when the app quits. */
  setInstallOnQuit(on: boolean): void;
}

/** The members of electron-updater's `AppUpdater` this module uses, plus the one it does not declare (`httpExecutor`). */
export type AppUpdaterLike = Pick<
  AppUpdater,
  'autoDownload' | 'autoInstallOnAppQuit' | 'autoRunAppAfterInstall' | 'allowDowngrade' | 'allowPrerelease' | 'requestHeaders' | 'logger' | 'setFeedURL' | 'checkForUpdates'
> & { httpExecutor?: { cachedSession?: unknown } };

export interface ElectronUpdaterOptions {
  /** `getUpdaterSession()`: the in-memory `updater` session that the egress guard opens for the GitHub hosts. */
  session: unknown;
  log?: Logger;
}

/**
 * Wraps electron-updater's `autoUpdater`. It refuses to work unless the library's HTTP executor can be given the guarded
 * session: left alone it would use its own `electron-updater` session, which the egress guard does not cover.
 */
export function createElectronUpdaterPort(updater: AppUpdaterLike, options: ElectronUpdaterOptions): UpdaterPort {
  const log = options.log ?? NULL_LOGGER;
  const executor = updater.httpExecutor;
  if (!executor || !('cachedSession' in executor)) throw new Error('electron-updater has no HTTP executor to put on the guarded session');
  executor.cachedSession = options.session;

  updater.autoDownload = true;
  updater.autoInstallOnAppQuit = true;
  updater.autoRunAppAfterInstall = true;
  updater.allowDowngrade = false;
  updater.allowPrerelease = false;
  // A "Cache-Control" header also stops the library from adding a random `?noCache=` query to every request.
  updater.requestHeaders = { 'Cache-Control': 'no-cache' };
  updater.logger = {
    info: (message?: unknown) => log.log('info', 'updates', String(message)),
    warn: (message?: unknown) => log.log('warn', 'updates', String(message)),
    error: (message?: unknown) => log.log('warn', 'updates', String(message)),
    debug: (message: string) => log.log('debug', 'updates', message),
  };

  let cancelDownload: (() => void) | null = null;
  return {
    setReleaseFolder: (url) => updater.setFeedURL({ provider: 'generic', url }),
    setInstallOnQuit: (on) => {
      updater.autoInstallOnAppQuit = on;
    },
    cancel: () => cancelDownload?.(),
    checkAndDownload: async () => {
      const result = await updater.checkForUpdates();
      if (result === null) return { available: false, version: '' };
      const version = result.updateInfo.version;
      if (!result.isUpdateAvailable) return { available: false, version };
      cancelDownload = () => result.cancellationToken?.cancel();
      try {
        await result.downloadPromise;
      } finally {
        cancelDownload = null;
      }
      return { available: true, version };
    },
  };
}

/**
 * The only place `electron-updater` is loaded. A dynamic import, so the bundler keeps the library in a chunk of its own
 * that no code reads until the first allowed check (the unit test `updates.test.ts` and `packaged-app.mjs verify`
 * check that nothing else imports it).
 */
export async function loadElectronUpdater(options: ElectronUpdaterOptions): Promise<UpdaterPort> {
  const loaded = await import('electron-updater');
  return createElectronUpdaterPort(loaded.autoUpdater as unknown as AppUpdaterLike, options);
}

// ---------------------------------------------------------------------------------------------------------------------
// The controller
// ---------------------------------------------------------------------------------------------------------------------

export type UpdateState = 'off' | 'paused' | 'unavailable' | 'idle' | 'checking' | 'downloading' | 'ready' | 'error';

export interface UpdateStatus {
  state: UpdateState;
  /** `paused`: why. */
  pausedBecause?: Exclude<GateReason, 'allowed' | 'setting-off'>;
  /** `downloading` and `ready`: the version. */
  version?: string;
  /** `error`: LAI-DSK-016. */
  code?: typeof DOCTOR_CODE;
  /** The last finished check (epoch ms), when there was one since the updates were switched on. */
  checkedAt?: number;
}

export interface Timers {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

export interface UpdateControllerDeps {
  /** `app.isPackaged`: an unpackaged run (development) never checks. */
  packaged: boolean;
  currentVersion: string;
  /** Loads the updater. Called at most once, and only for the first allowed check. */
  loadUpdater(): Promise<UpdaterPort>;
  /** GETs a text document (the release feed). Rejects on a non-2xx answer. */
  fetchText(url: string): Promise<string>;
  /** Opens or closes the updater's egress allowance (`setEgressState({ ...state, updates })`). */
  setEgress(allowed: boolean): void;
  /** `runAsUpdater` of the egress guard: the only code that may reach the GitHub hosts. Default: runs `fn` as it is. */
  runAsUpdater?<T>(fn: () => T): T;
  log?: Logger;
  clock?: () => number;
  timers?: Timers;
  onStatus?(status: UpdateStatus): void;
}

const errorMessage = (error: unknown): string => (error instanceof Error ? error.message : String(error));

export class UpdateController {
  readonly #d: UpdateControllerDeps;
  readonly #log: Logger;
  readonly #timers: Timers;
  readonly #clock: () => number;
  readonly #run: <T>(fn: () => T) => T;
  #status: UpdateStatus = { state: 'off' };
  #active = false;
  #lastSetting: boolean | null = null;
  #timer: unknown = null;
  #generation = 0;
  #running = false;
  #port: Promise<UpdaterPort> | null = null;
  #disposed = false;
  #readyVersion: string | null = null;

  constructor(deps: UpdateControllerDeps) {
    this.#d = deps;
    this.#log = deps.log ?? NULL_LOGGER;
    this.#timers = deps.timers ?? {
      setTimeout: (fn, ms) => {
        const handle = setTimeout(fn, ms);
        handle.unref();
        return handle;
      },
      clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
    };
    this.#clock = deps.clock ?? Date.now;
    this.#run = deps.runAsUpdater ?? ((fn) => fn());
  }

  get status(): UpdateStatus {
    return { ...this.#status };
  }

  /** Whether updates are switched on now (the timer runs and the egress allowance is open). */
  get active(): boolean {
    return this.#active;
  }

  /**
   * Tells the controller the current setting and mode. Cheap and idempotent: index.ts calls it on every status change and
   * every settings write. Switching to allowed starts the timer, switching away stops everything.
   */
  apply(gate: UpdateGate): void {
    if (this.#disposed) return;
    const decision = updateGate(gate);
    const ticked = this.#lastSetting === false && gate.setting; // the person just ticked the box: check now, not in 30 s
    this.#lastSetting = gate.setting;
    if (decision.allowed && !this.#d.packaged) {
      this.#deactivate();
      this.#set({ state: 'unavailable' });
      return;
    }
    if (decision.allowed) {
      if (!this.#active) this.#activate(ticked ? 0 : FIRST_CHECK_DELAY_MS);
      return;
    }
    if (this.#active) this.#deactivate();
    if (decision.reason === 'setting-off') this.#set({ state: 'off' });
    else this.#set({ state: 'paused', pausedBecause: decision.reason as Exclude<GateReason, 'allowed' | 'setting-off'> });
  }

  dispose(): void {
    this.#disposed = true;
    this.#deactivate();
  }

  #set(next: UpdateStatus): void {
    const same = JSON.stringify(next) === JSON.stringify(this.#status);
    this.#status = next;
    if (!same) this.#d.onStatus?.(this.status);
  }

  #activate(firstDelayMs: number): void {
    this.#active = true;
    this.#d.setEgress(true);
    if (this.#status.state === 'off' || this.#status.state === 'paused' || this.#status.state === 'unavailable') this.#set({ state: 'idle' });
    this.#log.log('info', 'updates', 'update checks on', { next_check_in_ms: firstDelayMs });
    this.#schedule(firstDelayMs);
  }

  /** Off: the egress allowance closes first, so nothing that is still running can make a new connection. */
  #deactivate(): void {
    if (!this.#active) return;
    this.#active = false;
    this.#d.setEgress(false);
    this.#generation += 1;
    if (this.#timer !== null) this.#timers.clearTimeout(this.#timer);
    this.#timer = null;
    const port = this.#port;
    if (port) {
      void port.then((p) => {
        p.cancel();
        p.setInstallOnQuit(false);
      }, () => undefined);
    }
    this.#running = false;
    this.#log.log('info', 'updates', 'update checks off');
  }

  #schedule(delayMs: number): void {
    if (this.#timer !== null) this.#timers.clearTimeout(this.#timer);
    this.#timer = this.#timers.setTimeout(() => {
      this.#timer = null;
      if (!this.#active) return;
      // The next check is planned before this one starts: a slow or failed check never moves the 6 h rhythm.
      this.#schedule(CHECK_INTERVAL_MS);
      void this.#check();
    }, delayMs);
  }

  async #check(): Promise<void> {
    if (this.#running || !this.#active) return;
    this.#running = true;
    const generation = this.#generation;
    const stale = (): boolean => generation !== this.#generation || !this.#active;
    const resting = (): UpdateStatus =>
      this.#readyVersion !== null ? { state: 'ready', version: this.#readyVersion, checkedAt: this.#clock() } : { state: 'idle', checkedAt: this.#clock() };
    try {
      this.#set(this.#readyVersion !== null ? { state: 'checking', version: this.#readyVersion } : { state: 'checking' });
      const feed = await this.#run(() => this.#d.fetchText(RELEASES_FEED_URL));
      if (stale()) return;
      const release = newestDesktopRelease(tagsFromFeed(feed), this.#d.currentVersion);
      const have = this.#readyVersion === null ? null : parseSemver(this.#readyVersion);
      const want = release === null ? null : parseSemver(release.version);
      // Nothing newer than the running version, or the newest one is already downloaded: no further request.
      if (release === null || (have !== null && want !== null && compareSemver(want, have) <= 0)) {
        this.#set(resting());
        return;
      }
      const port = await this.#load();
      if (stale()) return;
      await this.#run(async () => {
        port.setInstallOnQuit(true);
        port.setReleaseFolder(`${RELEASE_FOLDER_URL}/${release.tag}`);
        this.#set({ state: 'downloading', version: release.version });
        const result = await port.checkAndDownload();
        if (stale()) return;
        if (result.available) {
          this.#readyVersion = result.version;
          this.#log.log('info', 'updates', `update ${result.version} downloaded; it installs when the app quits`);
        }
        this.#set(resting());
      });
    } catch (error: unknown) {
      if (stale()) return;
      this.#log.log('warn', 'updates', `update check or download failed: ${errorMessage(error)}`, { code: DOCTOR_CODE });
      this.#set({ state: 'error', code: DOCTOR_CODE, checkedAt: this.#clock() });
    } finally {
      if (generation === this.#generation) this.#running = false;
    }
  }

  #load(): Promise<UpdaterPort> {
    // A failed load is tried again at the next check.
    this.#port ??= this.#d.loadUpdater().catch((error: unknown) => {
      this.#port = null;
      throw error;
    });
    return this.#port;
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// The menu's status lines
// ---------------------------------------------------------------------------------------------------------------------

/** The lines under "Check for updates" in the menu: none when the updates are off, one for most states, two for LAI-DSK-016. */
export function describeUpdateStatus(status: UpdateStatus, lang: Lang): string[] {
  switch (status.state) {
    case 'off':
    case 'idle':
      return status.state === 'idle' && status.checkedAt !== undefined ? [pick(UPDATE_TEXT.upToDate, lang)] : [];
    case 'checking':
      return [pick(UPDATE_TEXT.checking, lang)];
    case 'downloading':
      return [fill(pick(UPDATE_TEXT.downloading, lang), { version: status.version ?? '' })];
    case 'ready':
      return [fill(pick(UPDATE_TEXT.ready, lang), { version: status.version ?? '' })];
    case 'paused':
      return [pick(status.pausedBecause === 'offline' ? UPDATE_TEXT.pausedOffline : status.pausedBecause === 'survival' ? UPDATE_TEXT.pausedSurvival : UPDATE_TEXT.pausedUnknown, lang)];
    case 'unavailable':
      return [pick(UPDATE_TEXT.unavailable, lang)];
    case 'error':
      return [`${DOCTOR_CODE} · ${pick(UPDATE_TEXT.errorTitle, lang)}`, pick(UPDATE_TEXT.errorFix, lang)];
  }
}
