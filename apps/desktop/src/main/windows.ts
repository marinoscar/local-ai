// Windows: the main window (a BaseWindow with a 48 px top bar view and one content view) and the Chat, Library and Maps
// windows (docs/specs/desktop.md sections 3.4 and 3.6).
//
// `WindowManager` is the one object the rest of the app talks to: the start-up sequence uses it as its `ContentHost`
// (showError, showAgentUi, reloadAgentUi), the tray (DSK-06) and survival UX (DSK-09) call show/hide/focus, `openChild`
// and `mainWindow()`, and the navigation guard opens child windows through it. It never touches Electron directly: a
// `WindowFactory` creates the windows and views, so everything below the factory is unit-tested with fakes
// (tests/unit/windows.test.ts); `createElectronWindowFactory` is the thin real one.
import { join } from 'node:path';
import { BaseWindow, BrowserWindow, WebContentsView } from 'electron';
import { AGENT_HOST, AGENT_PORT, CLOUD_HOST, PARTITIONS } from './egress-guard';
import type { Lang } from './i18n';
import { CHANNELS, sanitizeBounds } from './ipc';
import { NULL_LOGGER, type Logger } from './log';
import { INTERNET_OFF_MESSAGE, type View, hardenedWebPreferences, registerView, setNavigationHooks } from './navigation';
import type { ContentHost } from './startup';
import type { DskCode } from './i18n';
import type { ChildWindowName, Tab, TopBarNotice, WindowBounds } from '../shared/types';

export const MODULE = 'windows' as const;

export const MAIN_WINDOW = { title: 'Local AI', width: 1280, height: 860, minWidth: 900, minHeight: 600 } as const;
export const TOP_BAR_HEIGHT = 48;
export const BOUNDS_SAVE_DELAY_MS = 500;
const AGENT_ORIGIN = `http://${AGENT_HOST}:${AGENT_PORT}`;

/** The child windows: where they open and how big. `chat/`, `library/` and `maps/` are the agent UI's own paths. */
export const CHILD_WINDOWS: Readonly<Record<ChildWindowName, { path: string; width: number; height: number; title: { en: string; es: string } }>> = {
  chat: { path: '/chat/', width: 1000, height: 780, title: { en: 'Local AI: Chat', es: 'Local AI: Chat' } },
  library: { path: '/library/', width: 1100, height: 800, title: { en: 'Local AI: Library', es: 'Local AI: Biblioteca' } },
  maps: { path: '/maps/', width: 1100, height: 800, title: { en: 'Local AI: Maps', es: 'Local AI: Mapas' } },
};

// ---------------------------------------------------------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------------------------------------------------------

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface DisplayArea {
  /** The part of the display the window may use (Electron's `Display.workArea`: without panels and docks). */
  workArea: Rect;
}

export interface ResolvedBounds {
  /** Absent when nothing was saved: the window manager places the window. */
  x?: number;
  y?: number;
  width: number;
  height: number;
  maximized: boolean;
}

/** Part of a window that must be on a display for the window to count as reachable: its title bar can be grabbed. */
const VISIBLE_MIN = { width: 100, height: 50 } as const;

function overlap(a: Rect, b: Rect): { width: number; height: number } {
  return {
    width: Math.max(0, Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x)),
    height: Math.max(0, Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y)),
  };
}

/**
 * The bounds to open the main window with: the saved ones, clamped to a display that exists now (desktop.md 3.8).
 * - nothing valid saved: the default size (shrunk to the primary display's work area when that is smaller), no position;
 * - saved, and enough of the window is on some display: moved and shrunk so it lies wholly inside the display it
 *   overlaps most;
 * - saved, but its display is gone: centred on the primary display.
 * The size never goes below the minimum, unless the display itself is smaller.
 */
export function resolveBounds(saved: WindowBounds | undefined, displays: readonly DisplayArea[], primary: DisplayArea): ResolvedBounds {
  const s = sanitizeBounds(saved);
  const maximized = s.maximized === true;
  let width = s.width ?? MAIN_WINDOW.width;
  let height = s.height ?? MAIN_WINDOW.height;
  const hasPosition = s.x !== undefined && s.y !== undefined;

  let area = primary.workArea;
  let onScreen = false;
  if (hasPosition) {
    const wanted: Rect = { x: s.x as number, y: s.y as number, width, height };
    let best = 0;
    for (const display of displays) {
      const o = overlap(wanted, display.workArea);
      if (o.width >= VISIBLE_MIN.width && o.height >= VISIBLE_MIN.height && o.width * o.height > best) {
        best = o.width * o.height;
        area = display.workArea;
        onScreen = true;
      }
    }
  }

  width = Math.min(Math.max(width, MAIN_WINDOW.minWidth), area.width);
  height = Math.min(Math.max(height, MAIN_WINDOW.minHeight), area.height);
  if (!hasPosition) return { width, height, maximized };
  if (!onScreen) {
    return { x: area.x + Math.floor((area.width - width) / 2), y: area.y + Math.floor((area.height - height) / 2), width, height, maximized };
  }
  const x = Math.min(Math.max(s.x as number, area.x), area.x + area.width - width);
  const y = Math.min(Math.max(s.y as number, area.y), area.y + area.height - height);
  return { x, y, width, height, maximized };
}

// ---------------------------------------------------------------------------------------------------------------------
// What the manager needs from Electron
// ---------------------------------------------------------------------------------------------------------------------

export interface WebContentsLike {
  readonly id: number;
  getURL(): string;
  loadURL(url: string): Promise<void>;
  reloadIgnoringCache(): void;
  isDestroyed(): boolean;
  send(channel: string, ...args: unknown[]): void;
  /** Present on a real `WebContents` (DSK-09 sets the Portal's stored language through it). */
  executeJavaScript?(code: string): Promise<unknown>;
}

export interface ViewLike {
  readonly webContents: WebContentsLike;
  setBounds(bounds: Rect): void;
}

export type MainWindowEvent = 'resize' | 'move' | 'maximize' | 'unmaximize' | 'enter-full-screen' | 'leave-full-screen';

export interface MainWindowLike {
  readonly contentView: { addChildView(view: ViewLike): void; removeChildView(view: ViewLike): void };
  getContentBounds(): Rect;
  /** The bounds the window has when neither maximized nor full screen. */
  getNormalBounds(): Rect;
  isMaximized(): boolean;
  maximize(): void;
  isFullScreen(): boolean;
  setFullScreen(flag: boolean): void;
  isVisible(): boolean;
  isMinimized(): boolean;
  isDestroyed(): boolean;
  show(): void;
  hide(): void;
  restore(): void;
  focus(): void;
  close(): void;
  onEvent(event: MainWindowEvent, listener: () => void): void;
  /** `preventDefault()` cancels the close. */
  onClose(listener: (event: { preventDefault(): void }) => void): void;
  onClosed(listener: () => void): void;
}

export interface ChildWindowLike {
  readonly webContents: WebContentsLike;
  isDestroyed(): boolean;
  isMinimized(): boolean;
  restore(): void;
  show(): void;
  focus(): void;
  close(): void;
  onClosed(listener: () => void): void;
}

/** The four kinds of view the main window holds, and what makes each one different (desktop.md 3.4). */
export type ViewKind = 'topbar' | 'agent' | 'lab' | 'error';

export const VIEW_SPECS: Readonly<Record<ViewKind, { partition: string | undefined; preload: 'shell.js' | 'agent.js'; view: View }>> = {
  topbar: { partition: undefined, preload: 'shell.js', view: 'shell' },
  agent: { partition: PARTITIONS.agent, preload: 'agent.js', view: 'content' },
  lab: { partition: PARTITIONS.lab, preload: 'shell.js', view: 'shell' },
  error: { partition: undefined, preload: 'shell.js', view: 'shell' },
};

export interface MainWindowInit {
  x?: number;
  y?: number;
  width: number;
  height: number;
  minWidth: number;
  minHeight: number;
  title: string;
}

export interface ChildWindowInit {
  width: number;
  height: number;
  title: string;
}

export interface WindowFactory {
  /** Creates the main window hidden. */
  createMainWindow(init: MainWindowInit): MainWindowLike;
  createView(kind: ViewKind): ViewLike;
  /** Creates a child window (partition `persist:agent`, no preload) and shows it. */
  createChildWindow(init: ChildWindowInit): ChildWindowLike;
}

// ---------------------------------------------------------------------------------------------------------------------
// The manager
// ---------------------------------------------------------------------------------------------------------------------

/** What the start-up sequence shows now in the local tab. */
export type LocalContent = 'none' | 'error' | 'agent-ui';

/** The typed window API of the app (shared with the tray, DSK-06, and survival UX, DSK-09). */
export interface WindowManager extends ContentHost {
  /**
   * Loads `http://127.0.0.1:7070<path>` in the agent view and shows it. `reveal: false` (DSK-09: leaving survival)
   * loads without showing a window that was closed to the tray.
   */
  showAgentUi(path: string, options?: { reveal?: boolean }): Promise<void>;

  // The main window
  /** Shows the main window (un-hides, restores, focuses). */
  showMain(): void;
  hideMain(): void;
  focusMain(): void;
  isMainVisible(): boolean;
  /** The raw main window, for what this API does not cover (DSK-09: full screen). */
  mainWindow(): MainWindowLike;
  /** When on, closing the main window hides it (a tray exists, DSK-06). Off: closing quits. */
  setHideOnClose(enabled: boolean): void;
  /** Call from `before-quit`: from then on closing really closes. */
  prepareToQuit(): void;
  /** The top bar's height (48; survival mode collapses it to 32, see survival.ts). */
  setTopBarHeight(pixels: number): void;

  // Tabs
  tab(): Tab;
  /** Shows the tab; `lab` is ignored while the Lab is not available. */
  showTab(tab: Tab): void;
  /** The Lab tab exists only in connected mode (desktop.md 4.9). Turning it off while it is shown returns to the local tab. */
  setLabAvailable(available: boolean): void;
  labAvailable(): boolean;
  /** Runs after the tab, the Lab availability or the local content changed. */
  onChange(listener: () => void): () => void;

  // Child windows
  /** Opens the window, or focuses it when it is already open (one each). */
  openChild(name: ChildWindowName): ChildWindowLike;
  childWindow(name: ChildWindowName): ChildWindowLike | null;
  closeChildren(): void;

  // Messages to pages
  sendToTopBar(channel: string, ...args: unknown[]): void;
  /** To every shell page of the main window that is showing an allowed shell URL (top bar, Lab, error page). */
  broadcastToShell(channel: string, ...args: unknown[]): void;

  // Test seams and recovery
  /** What the local tab shows. */
  current(): LocalContent;
  /** The path the agent view was last asked to load (`/`, `/portal`), or null. */
  agentPath(): string | null;
  agentContents(): WebContentsLike | null;
  topBarContents(): WebContentsLike;
}

export interface WindowManagerOptions {
  factory: WindowFactory;
  lang(): Lang;
  /** URL of a built shell page: `localai://app/error/?code=...` packaged, the Vite server in development. */
  shellPageUrl(page: string, query?: string): string;
  /** Is this URL a page that may receive pushed messages? (`isAllowedOrigin('shell', ...)`.) */
  isShellUrl(url: string): boolean;
  savedBounds?: WindowBounds;
  /** Writes the bounds to `settings.window.bounds`. */
  saveBounds?(bounds: WindowBounds): Promise<void>;
  displays(): { all: readonly DisplayArea[]; primary: DisplayArea };
  /** The app quits when the main window really closes. */
  quitApp(): void;
  /** `--hidden` (DSK-07): the window stays hidden until `showMain()`. */
  startHidden?: boolean;
  saveDelayMs?: number;
  log?: Logger;
}

class Manager implements WindowManager {
  readonly #o: WindowManagerOptions;
  readonly #log: Logger;
  readonly #main: MainWindowLike;
  readonly #topBar: ViewLike;
  readonly #views: Partial<Record<ViewKind, ViewLike>> = {};
  readonly #children = new Map<ChildWindowName, ChildWindowLike>();
  readonly #listeners = new Set<() => void>();
  #attached: ViewLike | null = null;
  #barHeight: number = TOP_BAR_HEIGHT;
  #local: LocalContent = 'none';
  #tab: Tab = 'local';
  #labAvailable = false;
  #labLoaded = false;
  #agentPath: string | null = null;
  #revealed: boolean;
  #hideOnClose = false;
  #quitting = false;
  #closeReady = false;
  #closed = false;
  #timer: ReturnType<typeof setTimeout> | null = null;

  constructor(options: WindowManagerOptions) {
    this.#o = options;
    this.#log = options.log ?? NULL_LOGGER;
    this.#revealed = options.startHidden !== true;

    const { all, primary } = options.displays();
    const bounds = resolveBounds(options.savedBounds, all, primary);
    this.#main = options.factory.createMainWindow({
      ...(bounds.x !== undefined && bounds.y !== undefined ? { x: bounds.x, y: bounds.y } : {}),
      width: bounds.width,
      height: bounds.height,
      minWidth: MAIN_WINDOW.minWidth,
      minHeight: MAIN_WINDOW.minHeight,
      title: MAIN_WINDOW.title,
    });
    if (bounds.maximized) this.#main.maximize();

    // The top bar loads at once. Its page also keeps the DevTools protocol answering: a webContents that never
    // navigated hangs Playwright's launch (the DSK-03 lesson).
    this.#topBar = this.#makeView('topbar');
    this.#main.contentView.addChildView(this.#topBar);
    this.#layout();
    void this.#topBar.webContents.loadURL(options.shellPageUrl('topbar')).catch(() => undefined);

    this.#main.onEvent('resize', () => this.#layout());
    for (const event of ['resize', 'move', 'maximize', 'unmaximize'] as const) this.#main.onEvent(event, () => this.#scheduleSave());
    this.#main.onClose((event) => this.#onClose(event));
    this.#main.onClosed(() => this.#onClosed());
  }

  // -- ContentHost ------------------------------------------------------------------------------------------------

  async showError(code: DskCode, extra?: { unit?: string }): Promise<void> {
    const view = this.#view('error');
    this.#local = 'error';
    this.#tab = 'local';
    this.#sync();
    this.#reveal();
    const query = new URLSearchParams({ code, lang: this.#o.lang(), ...(extra?.unit !== undefined ? { unit: extra.unit } : {}) });
    await view.webContents.loadURL(this.#o.shellPageUrl('error', `?${query.toString()}`)).catch(() => undefined);
  }

  async showAgentUi(path: string, options: { reveal?: boolean } = {}): Promise<void> {
    const view = this.#view('agent');
    this.#local = 'agent-ui';
    this.#agentPath = path;
    this.#tab = 'local';
    this.#sync();
    if (options.reveal !== false) this.#reveal();
    await view.webContents.loadURL(`${AGENT_ORIGIN}${path}`).catch(() => undefined);
  }

  reloadAgentUi(): void {
    const contents = this.#views.agent?.webContents;
    if (contents && !contents.isDestroyed()) contents.reloadIgnoringCache();
  }

  // -- main window ------------------------------------------------------------------------------------------------

  showMain(): void {
    if (this.#closed) return;
    this.#revealed = true;
    if (this.#main.isMinimized()) this.#main.restore();
    if (!this.#main.isVisible()) this.#main.show();
    this.#main.focus();
  }

  hideMain(): void {
    if (!this.#closed) this.#main.hide();
  }

  focusMain(): void {
    this.showMain();
  }

  isMainVisible(): boolean {
    return !this.#closed && this.#main.isVisible();
  }

  mainWindow(): MainWindowLike {
    return this.#main;
  }

  setHideOnClose(enabled: boolean): void {
    this.#hideOnClose = enabled;
  }

  prepareToQuit(): void {
    this.#quitting = true;
  }

  setTopBarHeight(pixels: number): void {
    if (!Number.isFinite(pixels) || pixels < 0) return;
    this.#barHeight = Math.round(pixels);
    this.#layout();
  }

  // -- tabs -------------------------------------------------------------------------------------------------------

  tab(): Tab {
    return this.#tab;
  }

  showTab(tab: Tab): void {
    if (tab === 'lab' && !this.#labAvailable) return;
    if (tab === this.#tab) return;
    this.#tab = tab;
    if (tab === 'lab') {
      const view = this.#view('lab');
      if (!this.#labLoaded) {
        this.#labLoaded = true;
        void view.webContents.loadURL(this.#o.shellPageUrl('lab')).catch(() => undefined);
      }
    }
    this.#sync(); // emits
  }

  setLabAvailable(available: boolean): void {
    if (available === this.#labAvailable) return;
    this.#labAvailable = available;
    if (!available && this.#tab === 'lab') this.showTab('local');
    else this.#emit();
  }

  labAvailable(): boolean {
    return this.#labAvailable;
  }

  onChange(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  // -- child windows ----------------------------------------------------------------------------------------------

  openChild(name: ChildWindowName): ChildWindowLike {
    const existing = this.#children.get(name);
    if (existing && !existing.isDestroyed()) {
      if (existing.isMinimized()) existing.restore();
      existing.show();
      existing.focus();
      return existing;
    }
    const spec = CHILD_WINDOWS[name];
    const win = this.#o.factory.createChildWindow({ width: spec.width, height: spec.height, title: spec.title[this.#o.lang()] });
    registerView(win.webContents as never, name);
    this.#children.set(name, win);
    win.onClosed(() => {
      if (this.#children.get(name) === win) this.#children.delete(name);
    });
    void win.webContents.loadURL(`${AGENT_ORIGIN}${spec.path}`).catch(() => undefined);
    return win;
  }

  childWindow(name: ChildWindowName): ChildWindowLike | null {
    const win = this.#children.get(name);
    return win && !win.isDestroyed() ? win : null;
  }

  closeChildren(): void {
    for (const win of [...this.#children.values()]) if (!win.isDestroyed()) win.close();
    this.#children.clear();
  }

  // -- messages ---------------------------------------------------------------------------------------------------

  sendToTopBar(channel: string, ...args: unknown[]): void {
    const contents = this.#topBar.webContents;
    if (!contents.isDestroyed()) contents.send(channel, ...args);
  }

  broadcastToShell(channel: string, ...args: unknown[]): void {
    for (const kind of ['topbar', 'lab', 'error'] as const) {
      const view = kind === 'topbar' ? this.#topBar : this.#views[kind];
      const contents = view?.webContents;
      if (!contents || contents.isDestroyed() || !this.#o.isShellUrl(contents.getURL())) continue;
      contents.send(channel, ...args);
    }
  }

  // -- seams ------------------------------------------------------------------------------------------------------

  current(): LocalContent {
    return this.#local;
  }

  agentPath(): string | null {
    return this.#agentPath;
  }

  agentContents(): WebContentsLike | null {
    return this.#views.agent?.webContents ?? null;
  }

  topBarContents(): WebContentsLike {
    return this.#topBar.webContents;
  }

  // -- internals --------------------------------------------------------------------------------------------------

  #makeView(kind: ViewKind): ViewLike {
    const view = this.#o.factory.createView(kind);
    // Declared before the first navigation, so the navigation guard knows what the page is.
    registerView(view.webContents as never, VIEW_SPECS[kind].view);
    return view;
  }

  #view(kind: 'agent' | 'lab' | 'error'): ViewLike {
    return (this.#views[kind] ??= this.#makeView(kind));
  }

  /** Attaches the view the current tab and local content call for, and detaches the others. */
  #sync(): void {
    let wanted: ViewLike | null = null;
    if (this.#tab === 'lab' && this.#views.lab) wanted = this.#views.lab;
    else if (this.#local === 'error') wanted = this.#views.error ?? null;
    else if (this.#local === 'agent-ui') wanted = this.#views.agent ?? null;
    if (wanted !== this.#attached) {
      if (this.#attached) this.#main.contentView.removeChildView(this.#attached);
      if (wanted) this.#main.contentView.addChildView(wanted);
      this.#attached = wanted;
    }
    this.#layout();
    this.#emit();
  }

  #layout(): void {
    const { width, height } = this.#main.getContentBounds();
    const bar = Math.min(this.#barHeight, height);
    this.#topBar.setBounds({ x: 0, y: 0, width, height: bar });
    this.#attached?.setBounds({ x: 0, y: bar, width, height: Math.max(0, height - bar) });
  }

  #reveal(): void {
    if (this.#revealed && !this.#closed && !this.#main.isVisible()) this.#main.show();
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

  // -- bounds -----------------------------------------------------------------------------------------------------

  #currentBounds(): WindowBounds | null {
    if (this.#closed || this.#main.isDestroyed() || this.#main.isFullScreen()) return null;
    const r = this.#main.getNormalBounds();
    return { x: r.x, y: r.y, width: r.width, height: r.height, maximized: this.#main.isMaximized() };
  }

  #scheduleSave(): void {
    if (!this.#o.saveBounds) return;
    if (this.#timer) clearTimeout(this.#timer);
    this.#timer = setTimeout(() => {
      this.#timer = null;
      void this.#saveNow();
    }, this.#o.saveDelayMs ?? BOUNDS_SAVE_DELAY_MS);
  }

  async #saveNow(): Promise<void> {
    if (this.#timer) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    const bounds = this.#currentBounds();
    if (!bounds || !this.#o.saveBounds) return;
    try {
      await this.#o.saveBounds(bounds);
    } catch (error) {
      this.#log.log('warn', 'app', `the window bounds could not be saved: ${(error as Error).message}`);
    }
  }

  #onClose(event: { preventDefault(): void }): void {
    if (this.#hideOnClose && !this.#quitting) {
      event.preventDefault();
      this.#main.hide();
      return;
    }
    if (this.#closeReady) return;
    // A real close: save the bounds first (the settings write is asynchronous), then close again.
    event.preventDefault();
    this.#closeReady = true;
    void this.#saveNow().finally(() => {
      if (!this.#main.isDestroyed()) this.#main.close();
    });
  }

  #onClosed(): void {
    this.#closed = true;
    this.closeChildren();
    this.#o.quitApp();
  }
}

export function createWindowManager(options: WindowManagerOptions): WindowManager {
  return new Manager(options);
}

// ---------------------------------------------------------------------------------------------------------------------
// Navigation hooks (DSK-02 left them for this item)
// ---------------------------------------------------------------------------------------------------------------------

/** Only `https://ai.marin.cr/...` (no credentials, default port) may be handed to the default browser. */
export function isCloudUrl(raw: string): boolean {
  try {
    const url = new URL(raw);
    return url.protocol === 'https:' && url.hostname === CLOUD_HOST && url.port === '' && url.username === '' && url.password === '';
  } catch {
    return false;
  }
}

export interface NavigationWiring {
  openExternal(url: string): void;
  lang(): Lang;
  /** Connected mode is active (desktop.md 4.9). */
  connected(): boolean;
}

/** Links to `/chat/`, `/library/`, `/maps/` open their window; a blocked link tells the top bar; the cloud opens in the browser when connected. */
export function wireNavigationHooks(windows: Pick<WindowManager, 'openChild' | 'sendToTopBar'>, deps: NavigationWiring): void {
  setNavigationHooks({
    openChild: (name) => {
      windows.openChild(name);
    },
    openExternal: (url) => {
      if (deps.connected() && isCloudUrl(url)) deps.openExternal(url);
    },
    contentBlocked: () => {
      const notice: TopBarNotice = { kind: 'internet-off', text: INTERNET_OFF_MESSAGE[deps.lang()] };
      windows.sendToTopBar(CHANNELS.notice, notice);
    },
  });
}

// ---------------------------------------------------------------------------------------------------------------------
// The real factory
// ---------------------------------------------------------------------------------------------------------------------

export interface ElectronFactoryOptions {
  isPackaged: boolean;
  /** Absolute folder of the built preload scripts (`out/preload`). */
  preloadDir: string;
}

function adaptMainWindow(win: BaseWindow): MainWindowLike {
  return {
    contentView: {
      addChildView: (view) => win.contentView.addChildView(view as unknown as WebContentsView),
      removeChildView: (view) => win.contentView.removeChildView(view as unknown as WebContentsView),
    },
    getContentBounds: () => win.getContentBounds(),
    getNormalBounds: () => win.getNormalBounds(),
    isMaximized: () => win.isMaximized(),
    maximize: () => win.maximize(),
    isFullScreen: () => win.isFullScreen(),
    setFullScreen: (flag) => win.setFullScreen(flag),
    isVisible: () => win.isVisible(),
    isMinimized: () => win.isMinimized(),
    isDestroyed: () => win.isDestroyed(),
    show: () => win.show(),
    hide: () => win.hide(),
    restore: () => win.restore(),
    focus: () => win.focus(),
    close: () => win.close(),
    onEvent: (event, listener) => {
      (win as unknown as { on(e: string, l: () => void): void }).on(event, listener);
    },
    onClose: (listener) => {
      (win as unknown as { on(e: string, l: (event: { preventDefault(): void }) => void): void }).on('close', listener);
    },
    onClosed: (listener) => {
      (win as unknown as { on(e: string, l: () => void): void }).on('closed', listener);
    },
  };
}

function adaptChildWindow(win: BrowserWindow): ChildWindowLike {
  return {
    webContents: win.webContents,
    isDestroyed: () => win.isDestroyed(),
    isMinimized: () => win.isMinimized(),
    restore: () => win.restore(),
    show: () => win.show(),
    focus: () => win.focus(),
    close: () => win.close(),
    onClosed: (listener) => {
      win.on('closed', listener);
    },
  };
}

export function createElectronWindowFactory(options: ElectronFactoryOptions): WindowFactory {
  return {
    createMainWindow(init) {
      const win = new BaseWindow({
        ...(init.x !== undefined && init.y !== undefined ? { x: init.x, y: init.y } : {}),
        width: init.width,
        height: init.height,
        minWidth: init.minWidth,
        minHeight: init.minHeight,
        title: init.title,
        show: false,
      });
      // No default menu bar: it would offer Reload and the DevTools in a packaged app.
      win.removeMenu();
      return adaptMainWindow(win);
    },
    createView(kind) {
      const spec = VIEW_SPECS[kind];
      return new WebContentsView({
        webPreferences: {
          ...hardenedWebPreferences(options.isPackaged),
          ...(spec.partition !== undefined ? { partition: spec.partition } : {}),
          preload: join(options.preloadDir, spec.preload),
        },
      }) as unknown as ViewLike;
    },
    createChildWindow(init) {
      // Agent partition (the session cookie), no preload: the agent's own pages need no bridge here.
      const win = new BrowserWindow({
        width: init.width,
        height: init.height,
        minWidth: 480,
        minHeight: 360,
        title: init.title,
        webPreferences: { ...hardenedWebPreferences(options.isPackaged), partition: PARTITIONS.agent },
      });
      win.removeMenu();
      return adaptChildWindow(win);
    },
  };
}
