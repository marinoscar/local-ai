// The start-up sequence (docs/specs/desktop.md 4.1), the 401 recovery and the error-page actions.
// No Electron import: the window, the survival hook and the agent are injected, so the whole sequence is unit-tested.
import { checkApiVersion, type AgentClient, type TokenResult } from './agent-client';
import { type DskCode, type ErrorAction, ERROR_ACTIONS, ERROR_COPY, isDskCode } from './i18n';
import { type Logger, NULL_LOGGER } from './log';
import { type AgentSupervisor, codeForGroupDiagnosis } from './agent-supervisor';
import type { SystemActions, SystemFailure } from './system-actions';

export const MODULE = 'startup' as const;

/** What the sequence needs from the window. The window manager of `windows.ts` implements it. */
export interface ContentHost {
  /** Shows `localai://app/error/?code=...` (default session, shell page). */
  showError(code: DskCode, options?: { unit?: string }): Promise<void>;
  /** Loads `http://127.0.0.1:7070<path>` in the agent partition. */
  showAgentUi(path: string): Promise<void>;
  /** Reloads whatever the agent view shows (after a new session). */
  reloadAgentUi(): void;
}

export interface SurvivalHook {
  /**
   * Full screen, collapsed strip (survival.ts). Called when the node is in survival mode at start-up, before the Portal
   * is loaded. `banner` is set when the token file is unreadable: the Portal needs no token, so it is shown anyway and
   * the strip carries the code.
   */
  enter(options?: { banner?: DskCode }): void | Promise<void>;
}

export type StartupClient = Pick<AgentClient, 'health' | 'readToken' | 'createSession' | 'status' | 'recreateSession'>;

export interface StartupDeps {
  client: StartupClient;
  supervisor: Pick<AgentSupervisor, 'unitState' | 'diagnoseTokenAccess'>;
  host: ContentHost;
  survival: SurvivalHook;
  log?: Logger;
  sleep?: (ms: number) => Promise<void>;
}

export type StartupResult =
  | { screen: 'agent-ui'; mode: string | null }
  | { screen: 'survival'; banner?: DskCode }
  | { screen: 'error'; code: DskCode };

/** The token-file failures the Portal can ride out: it is served without a login (desktop.md 4.7 rule 5). */
const TOKEN_FILE_CODES: readonly DskCode[] = ['LAI-DSK-001', 'LAI-DSK-002', 'LAI-DSK-003', 'LAI-DSK-017'];

const wait = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

/** The error page for a token file that could not be read (group diagnosis on EACCES, desktop.md 4.2). */
export async function codeForTokenFailure(failure: Extract<TokenResult, { ok: false }>, supervisor: Pick<AgentSupervisor, 'diagnoseTokenAccess'>): Promise<DskCode> {
  if (failure.reason !== 'permission-denied') return 'LAI-DSK-003';
  const diagnosis = await supervisor.diagnoseTokenAccess();
  return codeForGroupDiagnosis(diagnosis.kind);
}

export async function runStartup(deps: StartupDeps): Promise<StartupResult> {
  const log = deps.log ?? NULL_LOGGER;
  const sleep = deps.sleep ?? wait;
  // The mode `/health` reports (no login needed); set once the agent answered.
  let agentMode: string | undefined;
  const fail = async (code: DskCode, why: string, options?: { unit?: string }): Promise<StartupResult> => {
    if (agentMode === 'survival' && TOKEN_FILE_CODES.includes(code)) {
      // Survival mode and no usable token: the emergency screen must not become an error page.
      log.log('warn', 'agent', `${why}; the node is in survival mode, so the Portal is shown without a session`, { code });
      await deps.survival.enter({ banner: code });
      await deps.host.showAgentUi('/portal');
      return { screen: 'survival', banner: code };
    }
    log.log(ERROR_COPY[code].severity === 'critical' || ERROR_COPY[code].severity === 'error' ? 'error' : 'warn', 'agent', why, { code });
    await deps.host.showError(code, options);
    return { screen: 'error', code };
  };

  // 1. Is the agent there? (1 s timeout, 3 tries 300 ms apart.)
  let probe = await deps.client.health();
  if (!probe.reachable) {
    const unit = await deps.supervisor.unitState();
    return fail('LAI-DSK-004', 'the agent does not answer', { unit });
  }
  // The agent answers before its start-up finished: give it a moment (mutations are accepted earlier, but be kind).
  for (let waited = 0; probe.reachable && probe.health.starting && waited < 10; waited += 1) {
    await sleep(1000);
    probe = await deps.client.health({ tries: 1 });
  }
  if (!probe.reachable) return fail('LAI-DSK-004', 'the agent stopped answering while starting', { unit: await deps.supervisor.unitState() });
  agentMode = probe.health.mode;
  if (checkApiVersion(probe.health.version) === 'mismatch') {
    return fail('LAI-DSK-006', 'the agent API major version is not supported', undefined);
  }

  // 2. The token (EACCES: group diagnosis; ENOENT: not created yet).
  const token = await deps.client.readToken();
  if (!token.ok) return fail(await codeForTokenFailure(token, deps.supervisor), `the token file cannot be used (${token.reason})`);

  // 3. The session. 401: re-read the token once; still 401: LAI-DSK-007.
  let session = await deps.client.createSession();
  for (let attempt = 0; !session.ok && session.kind === 'unavailable' && attempt < 5; attempt += 1) {
    await sleep(1000);
    session = await deps.client.createSession();
  }
  if (!session.ok && session.kind === 'unauthorized') {
    const again = await deps.client.readToken();
    if (!again.ok) return fail(await codeForTokenFailure(again, deps.supervisor), `the token file cannot be used any more (${again.reason})`);
    session = await deps.client.createSession();
  }
  if (!session.ok) {
    if (session.kind === 'token') return fail(await codeForTokenFailure(session.token, deps.supervisor), 'the token file cannot be used');
    if (session.kind === 'unreachable' || session.kind === 'unavailable') return fail('LAI-DSK-004', `the agent did not take the session (${session.kind})`, { unit: await deps.supervisor.unitState() });
    return fail('LAI-DSK-007', 'the agent refused the session', undefined);
  }

  // 4. The mode decides the screen.
  const status = await deps.client.status();
  const mode = status.ok ? (status.status.mode?.value ?? null) : null;
  if (mode === 'survival') {
    await deps.survival.enter();
    await deps.host.showAgentUi('/portal');
    return { screen: 'survival' };
  }
  await deps.host.showAgentUi('/');
  return { screen: 'agent-ui', mode };
}

// ---------------------------------------------------------------------------------------------------------------------
// 401 recovery
// ---------------------------------------------------------------------------------------------------------------------

export const RECOVERY_WINDOW_MS = 30_000;

/** Agent API paths whose 401 is part of logging in, not a sign that the session died. */
const LOGIN_PATHS = ['/api/v1/session', '/api/v1/health'];

export function isRecoverable401(url: string): boolean {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  if (!parsed.pathname.startsWith('/api/v1/')) return false;
  return !LOGIN_PATHS.some((p) => parsed.pathname === p || parsed.pathname.startsWith(`${p}/`));
}

export interface RecoveryDeps {
  client: Pick<AgentClient, 'recreateSession'>;
  supervisor: Pick<AgentSupervisor, 'unitState' | 'diagnoseTokenAccess'>;
  host: Pick<ContentHost, 'showError' | 'reloadAgentUi'>;
  /** False while the agent view shows an error page or the Portal: a 401 there is not the operator session's. */
  active(): boolean;
  log?: Logger;
  now?: () => number;
}

/**
 * `webRequest.onCompleted` saw a 401 from the agent API in the agent partition: re-read the token (it may have been
 * rotated), open a new session, reload the view. At most once per 30 s, so a persistent 401 cannot loop.
 */
export class SessionRecovery {
  readonly #deps: RecoveryDeps;
  #last = Number.NEGATIVE_INFINITY;
  #running = false;

  constructor(deps: RecoveryDeps) {
    this.#deps = deps;
  }

  async onUnauthorized(url: string): Promise<'recovered' | 'failed' | 'skipped'> {
    const now = (this.#deps.now ?? Date.now)();
    if (this.#running || now - this.#last < RECOVERY_WINDOW_MS || !isRecoverable401(url) || !this.#deps.active()) return 'skipped';
    this.#running = true;
    this.#last = now;
    const log = this.#deps.log ?? NULL_LOGGER;
    try {
      const result = await this.#deps.client.recreateSession();
      if (result.ok) {
        log.log('info', 'agent', 'the agent session was renewed after a 401');
        this.#deps.host.reloadAgentUi();
        return 'recovered';
      }
      const code: DskCode = result.kind === 'token' ? await codeForTokenFailure(result.token, this.#deps.supervisor) : result.kind === 'unauthorized' || result.kind === 'unexpected' ? 'LAI-DSK-007' : 'LAI-DSK-004';
      log.log('error', 'agent', 'the agent session could not be renewed', { code });
      await this.#deps.host.showError(code, code === 'LAI-DSK-004' ? { unit: await this.#deps.supervisor.unitState() } : undefined);
      return 'failed';
    } finally {
      this.#running = false;
    }
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// Error-page actions (the one IPC channel of the error page)
// ---------------------------------------------------------------------------------------------------------------------

export interface ActionDeps {
  host: Pick<ContentHost, 'showAgentUi'>;
  supervisor: Pick<AgentSupervisor, 'start'>;
  clipboardWrite(text: string): void;
  /** `gnome-session-quit --logout` when present; resolves false when it is not there. */
  logout(): Promise<boolean>;
  /** Opens `http://127.0.0.1:7070/` in the default browser (the LAI-DSK-007 fallback). */
  openAgentInBrowser(): Promise<void>;
  /** Restarts the start-up sequence (after "Start agent" or "Make a new token" worked). */
  retry(): void;
  /** The fixed privileged calls (system-actions.ts). Absent: those buttons answer `unavailable`. */
  system?: SystemActions;
  /** The native "Restart the computer?" dialog (main process, so a page cannot skip it). Absent: never restarts. */
  confirmRestart?(): Promise<boolean>;
  /** Is kiwix-serve answering on 127.0.0.1:8888? Absent: "Open the library only" answers `unavailable`. */
  libraryReachable?(): Promise<boolean>;
  /** Opens the library (`http://127.0.0.1:8888/library/`) in the default browser. */
  openLibraryInBrowser?(): Promise<void>;
  /** One session attempt with a freshly read token: true once the agent accepts it (after "Make a new token"). */
  sessionWorks?(): Promise<boolean>;
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
  log?: Logger;
}

export interface ActionResult {
  ok: boolean;
  code?: string;
  /** "Start agent" failed (LAI-DSK-005): the read-only `systemctl status` excerpt, already scrubbed. */
  statusExcerpt?: string;
  /** "Start agent" was pressed while a start was already running: nothing was done. */
  busy?: boolean;
  /** Why a button did not work: a fixed word (`StartFailure` of "Start agent", `SystemFailure`, `library-down`), never output. */
  reason?: string;
  /** "Restart the computer" was answered with Cancel: nothing was done. */
  cancelled?: boolean;
}

/** The agent re-reads its token file within 10 s of a change (aictl.md 4.21): wait a little longer than that. */
export const NEW_TOKEN_WAIT_MS = 15_000;
const NEW_TOKEN_POLL_MS = 1000;

export function parseActionRequest(payload: unknown): { action: ErrorAction; code: DskCode } | null {
  if (typeof payload !== 'object' || payload === null) return null;
  const { action, code } = payload as { action?: unknown; code?: unknown };
  if (typeof action !== 'string' || !(ERROR_ACTIONS as readonly string[]).includes(action) || !isDskCode(code)) return null;
  // The page may only ask for a button its own code has (shown, or shown after another one worked).
  const allowed: readonly string[] = [...ERROR_COPY[code].actions, ...(ERROR_COPY[code].followUp ?? [])];
  return allowed.includes(action) ? { action: action as ErrorAction, code } : null;
}

const systemFailure = (reason: SystemFailure): ActionResult => (reason === 'busy' ? { ok: false, busy: true } : { ok: false, reason });

export async function handleErrorAction(request: { action: ErrorAction; code: DskCode }, deps: ActionDeps): Promise<ActionResult> {
  switch (request.action) {
    case 'copy-command': {
      const command = ERROR_COPY[request.code].command;
      if (command === undefined) return { ok: false };
      deps.clipboardWrite(command);
      return { ok: true };
    }
    case 'logout':
      return { ok: await deps.logout() };
    case 'open-portal':
      // The Portal needs no login, so it works even when the token file is unreadable.
      await deps.host.showAgentUi('/portal');
      return { ok: true };
    case 'open-browser':
      await deps.openAgentInBrowser();
      return { ok: true };
    case 'start-agent': {
      if (deps.supervisor.start === undefined) return { ok: false, code: 'LAI-DSK-005' };
      const result = await deps.supervisor.start();
      if (result.ok) {
        // Continue the start-up sequence: token, session, status, events.
        deps.retry();
        return { ok: true };
      }
      if (result.code === undefined) return { ok: false, busy: true };
      return { ok: false, code: result.code, reason: result.reason, ...(result.statusExcerpt !== undefined ? { statusExcerpt: result.statusExcerpt } : {}) };
    }
    case 'allow-user': {
      if (deps.system === undefined) return { ok: false, reason: 'unavailable' };
      const result = await deps.system.allowUser();
      // The new group reaches this login only after logging out and in: the page then offers "Log out now".
      return result.ok ? { ok: true } : systemFailure(result.reason);
    }
    case 'repair-access': {
      if (deps.system === undefined) return { ok: false, reason: 'unavailable' };
      const result = await deps.system.repairAccess();
      if (!result.ok) return systemFailure(result.reason);
      // No new login is needed: the folder opens at once. Run the start-up sequence again (token, session, status).
      deps.retry();
      return { ok: true };
    }
    case 'rotate-token': {
      if (deps.system === undefined) return { ok: false, reason: 'unavailable' };
      const result = await deps.system.rotateToken();
      if (!result.ok) return systemFailure(result.reason);
      // The agent picks the new file up within 10 s. Wait for a session that works, then run the start-up sequence
      // again (it shows the dashboard, or this page again if the agent still refuses).
      const sleep = deps.sleep ?? wait;
      const now = deps.now ?? Date.now;
      if (deps.sessionWorks !== undefined) {
        const deadline = now() + NEW_TOKEN_WAIT_MS;
        while (!(await deps.sessionWorks()) && now() < deadline) await sleep(NEW_TOKEN_POLL_MS);
      }
      deps.retry();
      return { ok: true };
    }
    case 'open-library': {
      if (deps.libraryReachable === undefined || deps.openLibraryInBrowser === undefined) return { ok: false, reason: 'unavailable' };
      if (!(await deps.libraryReachable())) return { ok: false, reason: 'library-down' };
      await deps.openLibraryInBrowser();
      return { ok: true };
    }
    case 'restart-computer': {
      if (deps.system === undefined || deps.confirmRestart === undefined) return { ok: false, reason: 'unavailable' };
      if (!(await deps.confirmRestart())) return { ok: false, cancelled: true };
      const result = await deps.system.reboot();
      return result.ok ? { ok: true } : systemFailure(result.reason);
    }
  }
}

/** Only the error page itself may invoke the actions (the shell bridge exists in every shell page). */
export function isErrorPageUrl(url: string, devOrigin?: string): boolean {
  try {
    const parsed = new URL(url);
    if (parsed.protocol === 'localai:' && parsed.hostname === 'app') return parsed.pathname === '/error/' || parsed.pathname === '/error';
    return devOrigin !== undefined && parsed.origin === new URL(devOrigin).origin && parsed.pathname.startsWith('/error/');
  } catch {
    return false;
  }
}
