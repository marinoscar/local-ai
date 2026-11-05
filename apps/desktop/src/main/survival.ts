// Survival UX: the full-screen Survival Portal (docs/specs/desktop.md 4.7).
//
// When the node is in survival mode the app becomes the Portal: the main window goes full screen, the content view shows
// `http://127.0.0.1:7070/portal`, the 48 px top bar collapses to a 32 px strip (the renderer draws it from
// `DesktopStatus.survival`), and the tabs and the Lab are gone. The rules this file keeps:
//   - Never `setKiosk`: it blocks the system's own keys on some desktops and could trap a user. Full screen is an
//     ordinary window state, and "Exit full screen" only leaves it; it never changes the node's mode.
//   - Leaving survival happens in the Portal (agent PIN) or from the tray (desktop PIN, DSK-06/DSK-08). Nothing here
//     asks the agent for a mode change.
//   - Closing the window hides it (windows.ts does that when a tray exists); a tray click or a second launch brings the
//     full-screen Portal back (`bringForward`). Quit stays in the tray.
//   - The Portal is served without a login, so it also shows when the token file is unreadable: the strip then carries
//     the LAI-DSK code as a small banner, and the controller watches `/health` itself because the event stream needs the token.
// No Electron import: the window manager, the mode and the clock are injected, so the state machine is unit-tested with fakes.
import type { FeedMessage } from './agent-client';
import type { DskCode, Lang } from './i18n';
import { NULL_LOGGER, type Logger } from './log';
import { TOP_BAR_HEIGHT } from './windows';
import type { LocalContent, MainWindowLike, WebContentsLike } from './windows';
import type { SurvivalState, Tab } from '../shared/types';

export const MODULE = 'survival' as const;

/** The collapsed top bar. */
export const STRIP_HEIGHT = 32;
export const PORTAL_PATH = '/portal';
/** The Portal's own language choice (survival-portal.md 3.2); it reads it once, at start-up. */
export const PORTAL_LANG_KEY = 'lai.portal.lang';
/** While the token file is unreadable no event arrives; `/health` is read this often to notice that survival ended. */
export const DEGRADED_POLL_MS = 5000;
/** A `/status` read that disagrees with a `mode.changed` event this recent started before it and is ignored. */
export const EVENT_PRECEDENCE_MS = 3000;

/**
 * Why the Portal is in front: the node is in survival mode (`mode`: needs the PIN to leave), or someone asked for it while
 * it is not (`portal`: `--portal`; the strip then offers a way back that needs no PIN).
 */
export type SurvivalReason = 'mode' | 'portal';

export interface EnterOptions {
  /** Default: `mode` when the node is known to be in survival mode, else `portal`. */
  reason?: SurvivalReason;
  /** Set when the token file is unreadable (LAI-DSK-001 to 003). */
  banner?: DskCode;
  /** False when the caller loads `/portal` itself right after (the start-up sequence). Default true. */
  load?: boolean;
}

export type SurvivalWindow = Pick<MainWindowLike, 'isFullScreen' | 'setFullScreen' | 'isDestroyed' | 'onEvent'>;
export type PortalContents = Pick<WebContentsLike, 'getURL' | 'isDestroyed' | 'reloadIgnoringCache' | 'executeJavaScript'>;

/** The part of the window manager (windows.ts) this module drives. */
export interface SurvivalWindows {
  showMain(): void;
  mainWindow(): SurvivalWindow;
  setTopBarHeight(pixels: number): void;
  showTab(tab: Tab): void;
  showAgentUi(path: string, options?: { reveal?: boolean }): Promise<void>;
  current(): LocalContent;
  agentPath(): string | null;
  agentContents(): PortalContents | null;
}

export interface SurvivalDeps {
  windows: SurvivalWindows;
  /** The node's mode as last read from the agent. */
  currentMode(): string | undefined;
  /** The language picked in the strip (`settings.lang` when it is not `auto`), else null: the Portal then decides itself. */
  chosenLang(): Lang | null;
  /** `/health`'s mode without a login (degraded survival), null when the agent does not answer. */
  probeMode(): Promise<string | null>;
  /** Runs the whole start-up sequence again (after degraded survival ended there is no dashboard to load without a token). */
  restart(): void;
  clock?(): number;
  pollMs?: number;
  log?: Logger;
}

export interface SurvivalSnapshot {
  active: boolean;
  fullScreen: boolean;
  reason: SurvivalReason;
  banner: DskCode | null;
}

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null;

/**
 * Sets the Portal's stored language and reloads it when that changed. The Portal reads `lai.portal.lang` only at
 * start-up, so a reload is the one way to switch it; the page keeps its route. Anything but a Portal page is left alone.
 */
export async function setPortalLanguage(contents: PortalContents | null, lang: Lang): Promise<boolean> {
  if (!contents || contents.isDestroyed() || contents.executeJavaScript === undefined) return false;
  let path: string;
  try {
    path = new URL(contents.getURL()).pathname;
  } catch {
    return false;
  }
  if (path !== PORTAL_PATH && !path.startsWith(`${PORTAL_PATH}/`)) return false;
  const code = `(() => { try { const k = ${JSON.stringify(PORTAL_LANG_KEY)}; const v = ${JSON.stringify(lang)}; if (window.localStorage.getItem(k) === v) return false; window.localStorage.setItem(k, v); return true; } catch { return false; } })()`;
  const changed = await contents.executeJavaScript(code).catch(() => false);
  if (changed !== true) return false;
  contents.reloadIgnoringCache();
  return true;
}

export class SurvivalController {
  readonly #deps: SurvivalDeps;
  readonly #log: Logger;
  readonly #listeners = new Set<() => void>();
  #active = false;
  #reason: SurvivalReason = 'mode';
  #banner: DskCode | null = null;
  #lastEvent: { to: string; at: number } | null = null;
  #timer: ReturnType<typeof setInterval> | null = null;
  #polling = false;

  constructor(deps: SurvivalDeps) {
    this.#deps = deps;
    this.#log = deps.log ?? NULL_LOGGER;
    // The strip's button follows the window, however full screen was left (F11, the window manager, the button).
    const win = deps.windows.mainWindow();
    win.onEvent('enter-full-screen', () => this.#emit());
    win.onEvent('leave-full-screen', () => this.#emit());
  }

  get active(): boolean {
    return this.#active;
  }

  snapshot(): SurvivalSnapshot {
    const win = this.#deps.windows.mainWindow();
    return { active: this.#active, fullScreen: this.#active && !win.isDestroyed() && win.isFullScreen(), reason: this.#reason, banner: this.#banner };
  }

  /** The strip's state without the banner text (index.ts adds it in the display language). */
  state(bannerText: (code: DskCode) => string): SurvivalState {
    const s = this.snapshot();
    return { active: s.active, fullScreen: s.fullScreen, portalOnly: s.active && s.reason === 'portal', banner: s.banner !== null ? { code: s.banner, text: bannerText(s.banner) } : null };
  }

  /** Runs after the strip's state changed (index.ts pushes a new status to the top bar). */
  onChange(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  /**
   * Shows the full-screen Portal. Idempotent for events (the same mode seen twice does nothing, and a window the user
   * took out of full screen stays as it is); a request for the Portal itself (`reason: 'portal'`) brings it forward again.
   */
  async enter(options: EnterOptions = {}): Promise<void> {
    const { windows } = this.#deps;
    const reason: SurvivalReason = this.#deps.currentMode() === 'survival' ? 'mode' : (options.reason ?? 'portal');
    const asked = options.reason === 'portal';
    if (this.#active) {
      if (reason === 'mode' && this.#reason !== 'mode') {
        this.#reason = 'mode';
        this.#emit();
      }
      if (options.banner !== undefined && this.#banner === null) {
        this.#banner = options.banner;
        this.#startPolling();
        this.#emit();
      }
      if (asked) this.bringForward();
      return;
    }
    this.#active = true;
    this.#reason = options.reason === 'mode' ? 'mode' : reason;
    this.#banner = options.banner ?? null;
    this.#log.log('info', 'app', `survival: showing the full-screen Portal (${this.#reason})`, this.#banner !== null ? { code: this.#banner } : {});
    // The Lab, if it was in front, gives way to the agent's view before anything else (cloud requests are DSK-10's to stop).
    windows.showTab('local');
    windows.setTopBarHeight(STRIP_HEIGHT);
    windows.showMain();
    this.#goFullScreen();
    this.#startPolling();
    this.#emit();
    if (options.load !== false) await this.#loadPortal();
  }

  /** The node left survival mode: windowed, the 48 px top bar again, the dashboard. */
  async exit(): Promise<void> {
    if (!this.#active) return;
    const degraded = this.#banner !== null;
    this.#active = false;
    this.#banner = null;
    this.#stopPolling();
    this.#log.log('info', 'app', 'survival: back to the windowed dashboard');
    const win = this.#deps.windows.mainWindow();
    if (!win.isDestroyed() && win.isFullScreen()) win.setFullScreen(false);
    this.#deps.windows.setTopBarHeight(TOP_BAR_HEIGHT);
    this.#emit();
    // Without a token there is no dashboard to load: the start-up sequence shows the right error page.
    if (degraded) this.#deps.restart();
    else await this.#deps.windows.showAgentUi('/', { reveal: false });
  }

  /** A tray click, a notification click or a second launch: the window is shown, and in survival it is full screen again. */
  bringForward(): void {
    this.#deps.windows.showMain();
    if (this.#active) this.#goFullScreen();
  }

  /** The strip's button. Only the window changes; never the node's mode. */
  setFullScreen(flag: boolean): void {
    if (!this.#active) return;
    const win = this.#deps.windows.mainWindow();
    if (win.isDestroyed()) return;
    if (flag) this.#goFullScreen();
    else if (win.isFullScreen()) win.setFullScreen(false);
  }

  /** The strip's "back" button: only for a Portal that was asked for outside survival mode. In survival mode leaving needs the PIN. */
  closePortal(): void {
    if (!this.#active || this.#reason !== 'portal') return;
    void this.exit().catch((error: unknown) => this.#log.log('warn', 'app', `survival: leaving the Portal failed: ${(error as Error).message}`));
  }

  /** The agent view finished loading a Portal page: apply the language the person chose in the strip. */
  async portalLoaded(): Promise<void> {
    const lang = this.#deps.chosenLang();
    if (lang !== null) await setPortalLanguage(this.#deps.windows.agentContents(), lang);
  }

  /** `settings.lang` changed (the strip's EN/ES toggle, or the menu): the Portal follows. */
  async languageChanged(): Promise<void> {
    if (this.#active) await this.portalLoaded();
  }

  /** The agent feed: `mode.changed` and the status reads that follow it. */
  handle(message: FeedMessage): void {
    const now = (this.#deps.clock ?? Date.now)();
    if (message.kind === 'event') {
      if (message.event.type !== 'mode.changed') return;
      const to = isObject(message.event.data) ? message.event.data['to'] : undefined;
      if (typeof to !== 'string') return;
      this.#lastEvent = { to, at: now };
      this.#apply(to);
    } else if (message.kind === 'status') {
      const mode = message.status.mode?.value;
      if (typeof mode !== 'string') return;
      const last = this.#lastEvent;
      if (last !== null && last.to !== mode && now - last.at < EVENT_PRECEDENCE_MS) return;
      this.#apply(mode);
    }
  }

  dispose(): void {
    this.#stopPolling();
    this.#listeners.clear();
  }

  // -- internals --------------------------------------------------------------------------------------------------

  #apply(mode: string): void {
    const failed = (what: string) => (error: unknown) => this.#log.log('warn', 'app', `survival: ${what} failed: ${(error as Error).message}`);
    if (mode === 'survival') void this.enter({ reason: 'mode' }).catch(failed('entering'));
    else if (this.#active && this.#reason === 'mode') void this.exit().catch(failed('leaving'));
  }

  #goFullScreen(): void {
    const win = this.#deps.windows.mainWindow();
    if (!win.isDestroyed() && !win.isFullScreen()) win.setFullScreen(true);
  }

  async #loadPortal(): Promise<void> {
    const { windows } = this.#deps;
    // Already showing the Portal (the error page's "Open Survival Portal", a second launch): no reload.
    if (!(windows.current() === 'agent-ui' && (windows.agentPath() ?? '').startsWith(PORTAL_PATH))) await windows.showAgentUi(PORTAL_PATH);
    await this.portalLoaded();
  }

  #startPolling(): void {
    if (this.#banner === null || this.#timer !== null) return;
    this.#timer = setInterval(() => void this.#poll(), this.#deps.pollMs ?? DEGRADED_POLL_MS);
    this.#timer.unref?.();
  }

  #stopPolling(): void {
    if (this.#timer !== null) clearInterval(this.#timer);
    this.#timer = null;
  }

  async #poll(): Promise<void> {
    if (this.#polling) return;
    this.#polling = true;
    try {
      const mode = await this.#deps.probeMode().catch(() => null);
      if (mode !== null && mode !== 'survival' && this.#active && this.#reason === 'mode') await this.exit();
    } catch (error) {
      this.#log.log('warn', 'app', `survival: leaving the Portal failed: ${(error as Error).message}`);
    } finally {
      this.#polling = false;
    }
  }

  #emit(): void {
    for (const listener of this.#listeners) {
      try {
        listener();
      } catch {
        // A listener that throws must not stop the others.
      }
    }
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// The module-level seam (DSK-03 left `enter()` for the start-up sequence; DSK-07 calls it for `--portal`)
// ---------------------------------------------------------------------------------------------------------------------

let installed: SurvivalController | null = null;

/** index.ts installs the controller once the window exists; tests pass null to clear it. */
export function install(controller: SurvivalController | null): void {
  installed = controller;
}

/** Shows the full-screen Portal. A no-op before the controller is installed. */
export function enter(options?: EnterOptions): Promise<void> {
  return installed ? installed.enter(options) : Promise.resolve();
}

export function exit(): Promise<void> {
  return installed ? installed.exit() : Promise.resolve();
}

/** True when a controller took the request (it showed the window); false: the caller shows the window itself. */
export function bringForward(): boolean {
  if (!installed) return false;
  installed.bringForward();
  return true;
}
