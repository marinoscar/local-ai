// Navigation, window-open and permission policy (docs/specs/desktop.md sections 3.4 and 3.6).
//
// `decide(view, url, state)` is a pure function: the table of section 3.6 and nothing else. The Electron wiring below
// (`installWebContentsHardening`) only applies its answer. Opening child windows is windows.ts's job: this module returns
// the decision and calls the `NavigationHooks` that `wireNavigationHooks` (windows.ts) sets.
import type { App, Session, WebContents, WebPreferences } from 'electron';
import { AGENT_HOST, AGENT_PORT, CLOUD_HOST, isLoopbackHost, normalizeHost } from './egress-guard';

export const MODULE = 'navigation' as const;

/** The companion apps that open in their own window, by path prefix of the agent origin (`music`: issue #515). */
export const CHILD_PREFIXES = ['chat', 'library', 'maps', 'music'] as const;
export type ChildName = (typeof CHILD_PREFIXES)[number];

/**
 * Which kind of page a webContents shows:
 * `content` the agent UI in the main window; `chat`/`library`/`maps`/`music` the child windows;
 * `shell` everything on localai://app (top bar, Lab, error, PIN, connection info, link).
 */
export type View = 'content' | ChildName | 'shell';

export type Decision = 'allow' | `open-child:${ChildName}` | 'open-external' | 'block';

export interface NavState {
  /** Connected mode is active (desktop.md 4.9): `https://ai.marin.cr/*` may then open in the default browser. */
  connected: boolean;
  /** Unpackaged builds only: the Vite dev server's origin, which the `shell` view may use. */
  devOrigin?: string;
  /** Unpackaged builds only: `devtools://` is allowed. */
  dev?: boolean;
}

/** What the top bar shows when a link from the agent UI is blocked (section 3.6). */
export const INTERNET_OFF_MESSAGE = {
  en: 'Links to the internet are turned off on this computer.',
  es: 'Los enlaces a internet están desactivados en esta computadora.',
} as const;

const AGENT_ORIGIN = `http://${AGENT_HOST}:${AGENT_PORT}`;
/** Static assets of the agent that every child window may navigate to (agent.md 3.11: the SPA bundle). */
const STATIC_PREFIXES = ['/assets/', '/portal/assets/'] as const;

function parse(raw: string): URL | null {
  try {
    return new URL(raw);
  } catch {
    return null;
  }
}

/** `http://127.0.0.1:7070` or `http://localhost:7070`, no credentials. `localhost` is allowed only to be rewritten. */
function isAgentUrl(url: URL): boolean {
  if (url.protocol !== 'http:' || url.port !== String(AGENT_PORT)) return false;
  if (url.username !== '' || url.password !== '') return false;
  const host = normalizeHost(url.hostname);
  return host === AGENT_HOST || host === 'localhost';
}

/** The segment-boundary prefix test: `/chat` and `/chat/x` are under `chat`, `/chatter` is not. */
function underPrefix(path: string, prefix: string): boolean {
  return path === `/${prefix}` || path.startsWith(`/${prefix}/`);
}

function childOf(path: string): ChildName | null {
  return CHILD_PREFIXES.find((name) => underPrefix(path, name)) ?? null;
}

/**
 * Rewrites `http://localhost:7070/...` to `http://127.0.0.1:7070/...` (the session layer allows only the latter).
 * Returns the normalised URL for any agent URL, or null when `raw` is not an agent URL.
 */
export function rewriteAgentUrl(raw: string): string | null {
  const url = parse(raw);
  if (!url || !isAgentUrl(url)) return null;
  return `${AGENT_ORIGIN}${url.pathname}${url.search}${url.hash}`;
}

/** The section 3.6 table. `url` is normalised with `new URL()` first, so dot segments are resolved before any prefix test. */
export function decide(view: View, raw: string, state: NavState): Decision {
  const url = parse(raw);
  if (!url) return 'block';

  // devtools:// (unpackaged builds) is the debugger, not a page.
  if (url.protocol === 'devtools:') return state.dev ? 'allow' : 'block';

  if (view === 'shell') {
    if (url.protocol === 'localai:' && url.hostname === 'app' && url.username === '' && url.password === '') return 'allow';
    if (state.devOrigin) {
      const dev = parse(state.devOrigin);
      if (dev && url.origin === dev.origin && isLoopbackHost(url.hostname)) return 'allow';
    }
    return 'block';
  }

  if (isAgentUrl(url)) {
    const path = url.pathname;
    if (view === 'content') {
      const child = childOf(path);
      return child ? `open-child:${child}` : 'allow';
    }
    // A child window: its own prefix, the API and the static assets; everything else is blocked.
    if (underPrefix(path, view) || path.startsWith('/api/v1/') || STATIC_PREFIXES.some((p) => path.startsWith(p))) return 'allow';
    return 'block';
  }

  if (view === 'content') {
    const secure = url.protocol === 'https:' && url.username === '' && url.password === '';
    if (secure && url.port === '' && normalizeHost(url.hostname) === CLOUD_HOST) return state.connected ? 'open-external' : 'block';
  }
  return 'block';
}

/** Navigation inside a frame never opens windows: only an allowed in-page destination passes. */
export function decideFrame(view: View, raw: string, state: NavState): 'allow' | 'block' {
  const decision = decide(view, raw, state);
  if (decision === 'allow') return 'allow';
  // An iframe of the agent UI pointing at /library/ or /maps/ stays inside the frame (same origin).
  if (view === 'content' && decision.startsWith('open-child:')) return 'allow';
  return 'block';
}

// ---------------------------------------------------------------------------------------------------------------------
// Permissions (section 3.4)
// ---------------------------------------------------------------------------------------------------------------------

export interface PermissionQuery {
  permission: string;
  requestingUrl: string;
  /** For `media`: the requested kinds (`audio`, `video`). */
  mediaTypes?: readonly string[];
  /** The view of the asking webContents, when known: the Music window never gets the microphone, whatever URL it shows. */
  view?: View;
  dev?: boolean;
  devOrigin?: string;
}

/**
 * Denied by default. Granted: `clipboard-sanitized-write` to shell pages, and `media` for the microphone only
 * (audio, never video) to the agent origin's speech page. The Music pages (`/music/`, issue #515) never get the
 * microphone: a sample is a file the person picks, not a recording.
 */
export function decidePermission(query: PermissionQuery): boolean {
  const url = parse(query.requestingUrl);
  if (!url) return false;
  if (query.permission === 'clipboard-sanitized-write') {
    if (url.protocol === 'localai:' && url.hostname === 'app') return true;
    if (query.dev && query.devOrigin) {
      const dev = parse(query.devOrigin);
      return dev !== null && url.origin === dev.origin;
    }
    return false;
  }
  if (query.permission === 'media') {
    const kinds = query.mediaTypes ?? [];
    if (url.origin !== AGENT_ORIGIN || query.view === 'music' || underPrefix(url.pathname, 'music')) return false;
    return kinds.length > 0 && kinds.every((kind) => kind === 'audio');
  }
  return false;
}

/** Permission request, check and device handlers on one session. Electron approves everything when none is set. */
export function installPermissionHandlers(ses: Pick<Session, 'setPermissionRequestHandler' | 'setPermissionCheckHandler' | 'setDevicePermissionHandler'>, dev: { dev?: boolean; devOrigin?: string } = {}): void {
  // Only a registered view is passed on: an unregistered webContents reads as `shell`, which says nothing here.
  const viewFor = (contents: WebContents | null): View | undefined => (contents && views.has(contents) ? views.get(contents) : undefined);
  ses.setPermissionRequestHandler((contents, permission, callback, details) => {
    const d = details as { requestingUrl?: string; mediaTypes?: string[] };
    const view = viewFor(contents);
    callback(decidePermission({ permission, requestingUrl: d.requestingUrl ?? '', mediaTypes: d.mediaTypes, ...(view ? { view } : {}), ...dev }));
  });
  ses.setPermissionCheckHandler((contents, permission, requestingOrigin, details) => {
    const d = details as { requestingUrl?: string; mediaType?: string };
    const view = viewFor(contents);
    return decidePermission({
      permission,
      ...(view ? { view } : {}),
      requestingUrl: d.requestingUrl ?? requestingOrigin,
      mediaTypes: d.mediaType === 'audio' ? ['audio'] : [],
      ...dev,
    });
  });
  ses.setDevicePermissionHandler(() => false);
}

// ---------------------------------------------------------------------------------------------------------------------
// webPreferences (section 3.4)
// ---------------------------------------------------------------------------------------------------------------------

/** The webPreferences of every webContents. A caller adds only `preload` and `partition`. */
export function hardenedWebPreferences(isPackaged: boolean): WebPreferences {
  return {
    contextIsolation: true,
    sandbox: true,
    nodeIntegration: false,
    webSecurity: true,
    spellcheck: false,
    devTools: !isPackaged,
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------------------------------------------------

/** What windows.ts plugs in (`wireNavigationHooks`). The defaults do nothing, so a blocked link is simply blocked. */
export interface NavigationHooks {
  openChild(name: ChildName): void;
  openExternal(url: string): void;
  /** Content view only: tell the top bar to show INTERNET_OFF_MESSAGE. */
  contentBlocked(contents: WebContents, url: string): void;
}

let hooks: NavigationHooks = { openChild: () => undefined, openExternal: () => undefined, contentBlocked: () => undefined };
export function setNavigationHooks(next: Partial<NavigationHooks>): void {
  hooks = { ...hooks, ...next };
}

const views = new WeakMap<WebContents, View>();
/** Tells the navigation guard what a webContents shows. An unregistered webContents is treated as `shell` (strictest). */
export function registerView(contents: WebContents, view: View): void {
  views.set(contents, view);
}
export function viewOf(contents: WebContents): View {
  return views.get(contents) ?? 'shell';
}

export interface HardeningOptions {
  getState(): NavState;
}

/** Applies `decide` to one navigation of one webContents. Returns what the event handler should do. */
export function applyNavigation(contents: WebContents, view: View, url: string, state: NavState, kind: 'navigate' | 'redirect' | 'open'): { prevent: boolean; loadInstead?: string } {
  const decision = decide(view, url, state);
  const rewritten = view === 'shell' ? null : rewriteAgentUrl(url);
  const needsRewrite = rewritten !== null && rewritten !== url;
  if (decision === 'allow') {
    if (needsRewrite) return { prevent: true, loadInstead: rewritten };
    // window.open to an allowed place loads it here: the handler denies the new window.
    return kind === 'open' ? { prevent: true, loadInstead: url } : { prevent: false };
  }
  if (kind !== 'redirect') {
    if (decision.startsWith('open-child:')) hooks.openChild(decision.slice('open-child:'.length) as ChildName);
    else if (decision === 'open-external') hooks.openExternal(parse(url)?.href ?? url);
    else if (view === 'content') hooks.contentBlocked(contents, url);
  }
  return { prevent: true };
}

/**
 * Hooks every webContents the app ever creates (`web-contents-created`), so a window added later is covered without
 * remembering to: will-navigate, will-frame-navigate, will-redirect, setWindowOpenHandler (always `deny`), no
 * `<webview>`, spell checking off. Register it before the first window.
 */
export function installWebContentsHardening(app: Pick<App, 'on'>, options: HardeningOptions): void {
  app.on('web-contents-created', (_event, contents) => {
    contents.setWindowOpenHandler(({ url }) => {
      const result = applyNavigation(contents, viewOf(contents), url, options.getState(), 'open');
      if (result.loadInstead) void contents.loadURL(result.loadInstead).catch(() => undefined);
      return { action: 'deny' };
    });
    contents.on('will-navigate', (event, url) => {
      const result = applyNavigation(contents, viewOf(contents), url, options.getState(), 'navigate');
      if (result.prevent) event.preventDefault();
      if (result.loadInstead) void contents.loadURL(result.loadInstead).catch(() => undefined);
    });
    contents.on('will-redirect', (event, url) => {
      const result = applyNavigation(contents, viewOf(contents), url, options.getState(), 'redirect');
      if (result.prevent) event.preventDefault();
      if (result.loadInstead) void contents.loadURL(result.loadInstead).catch(() => undefined);
    });
    contents.on('will-frame-navigate', (event) => {
      if (event.isMainFrame) return; // will-navigate and will-redirect handle the main frame
      if (decideFrame(viewOf(contents), event.url, options.getState()) === 'block') event.preventDefault();
    });
    contents.on('will-attach-webview', (event) => event.preventDefault());
    contents.session.setSpellCheckerEnabled(false);
  });
}
