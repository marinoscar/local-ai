// Types shared by the main process, the two preload bridges and the shell pages (docs/specs/desktop.md section 3.5).
// Everything here is plain data (structured-clone safe): it crosses the IPC boundary. No runtime code lives in this
// file, so a sandboxed preload can import it for the types without pulling a module into its bundle.

export type Lang = 'en' | 'es';
export type LangSetting = 'auto' | Lang;
/** The node's mode (modes.md). */
export type NodeMode = 'online' | 'offline' | 'survival';
/** The three companion apps that open in their own window. */
export type ChildWindowName = 'chat' | 'library' | 'maps';
/** The two content tabs of the top bar. */
export type Tab = 'local' | 'lab';

/** What a bridge method answers while its item has not landed (desktop.md 3.5 "As built (DSK-04)"). */
export interface NotImplemented {
  ok: false;
  code: 'not_implemented';
}

export interface ActionResult {
  ok: boolean;
  code?: string;
  /** "Start agent" failed (LAI-DSK-005): the read-only `systemctl status` excerpt, already scrubbed. */
  statusExcerpt?: string;
  /** "Start agent" was pressed while a start was already running: nothing was done. */
  busy?: boolean;
}

/** Can the desktop reach its agent? `starting` covers the start-up sequence and the first event connection. */
export type AgentReach = 'starting' | 'up' | 'down' | 'unauthorized';

/** What the desktop knows about the lab link (the PAT itself never leaves the main process). */
export interface CloudLinkState {
  /** `settings.cloud.linked`. */
  linked: boolean;
  /** A PAT is held in the main process's memory (DSK-10). */
  tokenHeld: boolean;
  url: string;
}

/**
 * What the top bar needs to turn into the survival strip (desktop.md 4.7). `active` is the desktop's own state (the Portal
 * is shown in survival form), not the agent's mode: it flips before the next `/status` is read.
 */
export interface SurvivalState {
  active: boolean;
  /** The main window is full screen now (it follows the window's events, so F11 and the window manager show up here). */
  fullScreen: boolean;
  /** The Portal was opened with `--portal` while the node is not in survival mode: the strip then offers a way back. */
  portalOnly: boolean;
  /** A small notice for the strip, in the display language (LAI-DSK-001 when the token file is unreadable). */
  banner: { code: string; text: string } | null;
}

export interface DesktopStatus {
  /** The app's version (the About dialog). */
  version: string;
  agent: AgentReach;
  /** The node's mode from the agent's `/status`; null until it was read. */
  mode: NodeMode | null;
  /** The agent's `connectivity.value` (`full`, `limited`, `none`, `not_checked`), null until read. */
  connectivity: string | null;
  /** Alerts of severity warning or worse in the agent's `/status`. */
  alerts: number;
  cloud: CloudLinkState;
  /** "Connected mode is active" (desktop.md 4.9): linked, a token held, and the node's mode is `online`. The Lab tab exists only then. */
  connected: boolean;
  /** The tab the main window shows. */
  tab: Tab;
  /** Absent in snapshots older than DSK-09; the top bar treats absent as "not in survival form". */
  survival?: SurvivalState;
}

/** DSK-08 owns the final shape (desktop.md 4.6); the key is masked until `revealKey`. */
export interface ConnectionInfo {
  localUrl: string;
  publicUrl: string | null;
  labUrl: string;
  keyMasked: string;
}

export interface CloudResponse {
  ok: boolean;
  status: number;
  body: unknown;
}

/** The bounds of the main window as saved in `settings.window.bounds` (desktop.md 3.8). Every field is optional: an empty object means "never saved". */
export interface WindowBounds {
  x?: number;
  y?: number;
  width?: number;
  height?: number;
  maximized?: boolean;
}

/** The part of `settings.json` a shell page may read. Keys this version does not know are never shown. */
export interface ShellSettings {
  autostart: boolean;
  updates: { checkForUpdates: boolean };
  lang: LangSetting;
  window: { bounds: WindowBounds };
  secrets: { allowPlaintext: boolean };
  /** Read only: `cloud.*` is written by the main process alone (DSK-10). */
  cloud: { url: string; linked: boolean };
}

/** The keys a shell page may change (`cloud.*` is not among them). */
export interface ShellSettingsPatch {
  autostart?: boolean;
  updates?: { checkForUpdates?: boolean };
  lang?: LangSetting;
  window?: { bounds?: WindowBounds };
  secrets?: { allowPlaintext?: boolean };
}

/** A message for the top bar from the main process (a blocked link, a feature that is not there yet). */
export interface TopBarNotice {
  kind: 'internet-off' | 'not-available';
  text: string;
}

/** `window.localaiDesktop` (shell pages only). The members after `lang` are the ones this item adds to the spec's list. */
export interface LocalaiDesktop {
  status(): Promise<DesktopStatus>;
  onStatus(cb: (status: DesktopStatus) => void): () => void;
  startAgent(): Promise<ActionResult | NotImplemented>;
  copyApiKey(): Promise<{ ok: boolean; clearsInS: number } | NotImplemented>;
  connectionInfo(): Promise<ConnectionInfo | NotImplemented>;
  revealKey(): Promise<string | NotImplemented>;
  setMode(target: NodeMode): Promise<ActionResult | NotImplemented>;
  openWindow(which: ChildWindowName): void;
  showTab(tab: Tab): void;
  /** Survival strip (DSK-09): full screen on or off. It never changes the node's mode. Ignored outside the survival form. */
  setFullScreen(flag: boolean): void;
  /** Survival strip (DSK-09): leave a Portal that was opened while the node is not in survival mode. Ignored in survival mode (that needs the PIN). */
  closePortal(): void;
  cloud: {
    beginLink(): Promise<{ userCode: string; verificationUri: string; expiresIn: number } | NotImplemented>;
    openVerification(): void;
    linkState(): Promise<CloudLinkState>;
    unlink(): Promise<void | NotImplemented>;
    request(req: { method: 'GET' | 'POST' | 'DELETE'; path: string; body?: unknown }): Promise<CloudResponse | NotImplemented>;
  };
  settings: { get(): Promise<ShellSettings>; set(patch: ShellSettingsPatch): Promise<ShellSettings> };
  pinSubmit(pin: string): Promise<ActionResult | NotImplemented>;
  lang(): Lang;
  // Added by DSK-04 (section 3.5 "As built"):
  /** A button of the error page (DSK-03). The main process checks the page and that the code owns the button. */
  errorAction(action: string, code: string): Promise<ActionResult>;
  /** The top bar's menu (the three dots): the main process pops up a native menu, which a 48 px view could not hold. */
  showMenu(): void;
  /** Messages for the top bar. */
  onNotice(cb: (notice: TopBarNotice) => void): () => void;
}

/** `window.localaiDesktopHost` (agent pages): exactly these five members (desktop.md 3.5). */
export interface LocalaiDesktopHost {
  readonly isDesktop: true;
  readonly version: string;
  copyApiKey(): Promise<{ ok: boolean; clearsInS: number } | NotImplemented>;
  openWindow(which: ChildWindowName): void;
  connectionInfo(): Promise<ConnectionInfo | NotImplemented>;
}

export const HOST_MEMBERS = ['isDesktop', 'version', 'copyApiKey', 'openWindow', 'connectionInfo'] as const;
