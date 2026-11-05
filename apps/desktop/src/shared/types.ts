// Types shared by the main process, the two preload bridges and the shell pages (docs/specs/desktop.md section 3.5).
// Everything here is plain data (structured-clone safe): it crosses the IPC boundary. No runtime code lives in this
// file, so a sandboxed preload can import it for the types without pulling a module into its bundle.

import type { LabSummaryDto, NodeCardDto } from '@localai/ui/lab/types';

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

/** Where a link attempt stands (desktop.md 4.8, "As built (DSK-10)"). `pending` lasts until the user approves, denies or the code expires. */
export type LinkPhase = 'pending' | 'saving' | 'done' | 'denied' | 'expired' | 'failed' | 'unreachable' | 'cancelled';

/** The progress of the latest link attempt; absent when there never was one in this run. Holds no code, no token. */
export interface LinkProgress {
  phase: LinkPhase;
  /** The doctor code to show with the phase: LAI-DSK-009 for `denied`/`expired`. */
  code?: string;
  /** The token was kept in memory only (no password keyring and no consent, LAI-DSK-008): it is gone after a restart. */
  memoryOnly?: boolean;
}

/** What the desktop knows about the lab link (the PAT itself never leaves the main process). */
export interface CloudLinkState {
  /** `settings.cloud.linked`. */
  linked: boolean;
  /** A PAT is held in the main process's memory (DSK-10). */
  tokenHeld: boolean;
  url: string;
  /** Who is linked: the account's display name, never an email address. Absent until the cloud answered `GET /api/auth/me`. */
  user?: string;
  /** When the PAT expires (ISO 8601). */
  expiresAt?: string;
  link?: LinkProgress;
}

/** `cloud.beginLink()` succeeded: the code to show and how long it lasts. */
export interface LinkBegin {
  userCode: string;
  /** Always `https://ai.marin.cr/activate`: the page the user opens on any phone or computer. */
  verificationUri: string;
  /** Seconds left. */
  expiresIn: number;
}

/** `cloud.beginLink()` was refused. `not_online`: the node is `offline`/`survival` ("Switch this computer to Online first."). */
export interface LinkRefusal {
  ok: false;
  code: 'not_online' | 'already_linked' | 'unreachable' | 'unavailable';
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

/** A refusal of a key or PIN call: the code is a doctor code (or the agent's), `fix` the one-sentence remedy when there is one. */
export interface KeyFailure {
  ok: false;
  code: string;
  message?: string;
  fix?: string;
}

/** `copyApiKey()`: the key is on the clipboard and will be cleared after `clearsInS` seconds. */
export type CopyKeyResult = { ok: true; clearsInS: number } | KeyFailure;

/**
 * The ready snippets of `GET /key` (agent.md 3.8.13). They hold the placeholders `$KEY` / `<key>`, never the key; the
 * connection-info page substitutes the key only after **Reveal** (src/renderer/connection-info/snippets.ts).
 */
export interface ConnectionSnippets {
  curl: string;
  env: string;
  /** The OpenCode provider block, as pretty-printed JSON. */
  opencode: string;
  openaiPython: string;
}

/** What the connection-info page shows (desktop.md 4.6). The key is masked (`ab12…9f3c`) until `revealKey`. */
export interface ConnectionInfo {
  ok: true;
  /** `http://127.0.0.1:8080/v1`: the one that keeps working without internet. */
  localUrl: string;
  tailnetUrl: string | null;
  /** The node's public URL; null when offline or not set. */
  publicUrl: string | null;
  /** `https://ai.marin.cr/v1`: the lab gateway, which needs a `lai_` key from the cloud, not this key. */
  labUrl: string;
  keyMasked: string;
  models: string[];
  snippets: ConnectionSnippets;
}

export type ConnectionInfoResult = ConnectionInfo | KeyFailure;

/** `revealKey()`: the full key, only for an explicit Reveal on the connection-info page. Each call re-reads `GET /key` (the agent audits it). */
export type RevealResult = { ok: true; key: string } | KeyFailure;

/** What `rotateKey()` answers (agent.md 3.8.14, plus the cases of the PIN window). */
export type RotateKeyResult =
  | {
      ok: true;
      fingerprint: string;
      rotatedAt: string;
      cloudSynced: boolean;
      restoredModels: string[];
      /** Where the old key may still be in use ("OpenCode on this computer: ..."). */
      clientsToUpdate: string[];
    }
  | (KeyFailure & {
      /** The user closed the PIN window: nothing was asked of the agent. */
      cancelled?: true;
      /** The agent changed the key although the call failed after that (`details.key_rotated`): the old key is dead. */
      keyRotated?: boolean;
      fingerprint?: string;
    });

/** The answer to a PIN typed in the PIN window (desktop.md 4.6). The PIN itself is never echoed. */
export interface PinResult {
  ok: boolean;
  /** `LAI-SEC-003` (wrong), `LAI-SEC-004` (locked), `LAI-SEC-005` (no PIN set), or a local word (`invalid_format`, `busy`, `no_prompt`). */
  code?: string;
  fix?: string;
  /** `LAI-SEC-003`: the failures the agent's 5-minute window still allows. */
  remainingAttempts?: number;
  /** `LAI-SEC-004`: seconds until the agent accepts another try. */
  retryAfterS?: number;
}

/** An answer from the cloud, as the proxy got it (EAB's `{ data }` envelope included). `ok` is a 2xx status. */
export interface CloudResponse {
  ok: boolean;
  status: number;
  body: unknown;
}

/**
 * Why a Lab request produced no answer (desktop.md 4.8, "As built (DSK-11)"). `refused`: the allowlist said no, nothing
 * was sent. `offline`: connected mode is not active (the node is `offline`/`survival`, or not linked). `aborted`: the
 * node left `online` while the request was in flight. `timeout`: no answer within 10 s. `unreachable`: network error.
 * `unauthorized`: the cloud answered 401 (the link was removed, LAI-DSK-011). `unavailable`: this build has no proxy.
 */
export type CloudFailureKind = 'refused' | 'offline' | 'aborted' | 'timeout' | 'unreachable' | 'unauthorized' | 'unavailable';

export interface CloudFailure {
  ok: false;
  error: CloudFailureKind;
  /** `refused` only: which check said no (`path`, `method`, `query`, `command_type`, `body`, `bad_request`). Never the text the page sent. */
  reason?: string;
}

/** `cloud.request()` answers a `CloudResponse` (the cloud replied, whatever the status) or a `CloudFailure` (it did not). */
export type CloudResult = CloudResponse | CloudFailure;

/** The node cards of the last good `GET /api/localai/nodes`, read back from `lab-cache.json` (no secrets, bounded). */
export interface LabCacheView {
  /** When the cards were fetched (ISO 8601). */
  savedAt: string;
  summary: LabSummaryDto;
  nodes: NodeCardDto[];
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
  copyApiKey(): Promise<CopyKeyResult>;
  connectionInfo(): Promise<ConnectionInfoResult>;
  revealKey(): Promise<RevealResult>;
  setMode(target: NodeMode): Promise<ActionResult | NotImplemented>;
  openWindow(which: ChildWindowName): void;
  showTab(tab: Tab): void;
  /** Survival strip (DSK-09): full screen on or off. It never changes the node's mode. Ignored outside the survival form. */
  setFullScreen(flag: boolean): void;
  /** Survival strip (DSK-09): leave a Portal that was opened while the node is not in survival mode. Ignored in survival mode (that needs the PIN). */
  closePortal(): void;
  cloud: {
    beginLink(): Promise<LinkBegin | LinkRefusal>;
    openVerification(): void;
    linkState(): Promise<CloudLinkState>;
    unlink(): Promise<void>;
    /** The Lab view only. An allowlisted (method, path) pair; anything else answers `{ ok: false, error: 'refused' }` with no network call. */
    request(req: { method: 'GET' | 'POST' | 'DELETE'; path: string; body?: unknown }): Promise<CloudResult>;
    /** The Lab view only (DSK-11): the last cached node cards, or null when there is none. Reads a local file; no network. */
    cached(): Promise<LabCacheView | null>;
    /** The Lab view only (DSK-11): opens `https://ai.marin.cr/lab/nodes/<nodeId>` in the default browser (connected mode only). */
    openNode(nodeId: string): void;
  };
  settings: { get(): Promise<ShellSettings>; set(patch: ShellSettingsPatch): Promise<ShellSettings> };
  pinSubmit(pin: string): Promise<PinResult>;
  lang(): Lang;
  // Added by DSK-04 (section 3.5 "As built"):
  /** A button of the error page (DSK-03). The main process checks the page and that the code owns the button. */
  errorAction(action: string, code: string): Promise<ActionResult>;
  /** The top bar's menu (the three dots): the main process pops up a native menu, which a 48 px view could not hold. */
  showMenu(): void;
  /** Messages for the top bar. */
  onNotice(cb: (notice: TopBarNotice) => void): () => void;
  // Added by DSK-08 (section 3.5 "As built"):
  /** "Rotate key..." of the connection-info page: asks for the PIN, then `POST /key/rotate` with the grant. */
  rotateKey(): Promise<RotateKeyResult>;
}

/** `window.localaiDesktopHost` (agent pages): exactly these five members (desktop.md 3.5). */
export interface LocalaiDesktopHost {
  readonly isDesktop: true;
  readonly version: string;
  copyApiKey(): Promise<CopyKeyResult>;
  openWindow(which: ChildWindowName): void;
  connectionInfo(): Promise<ConnectionInfoResult>;
}

export const HOST_MEMBERS = ['isDesktop', 'version', 'copyApiKey', 'openWindow', 'connectionInfo'] as const;

/** `window.localaiPin` (the PIN window only, src/preload/pin.ts): the smallest surface that can type a PIN and give up. */
export interface LocalaiPin {
  submit(pin: string): Promise<PinResult>;
  cancel(): void;
}
