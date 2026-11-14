// Agent supervisor: the agent's systemd unit state, the group-membership diagnosis and "Start agent"
// (docs/specs/desktop.md 4.2). DSK-03 owns the read-only parts; DSK-05 the start: `systemctl start` (the installer's
// polkit rule makes it silent), then `pkexec` (the system asks for the administrator password), then a 30 s wait for
// the agent's /health. No shell is ever used: `execFile` with fixed arguments, and no user input in any argument.
import { execFile } from 'node:child_process';
import { userInfo } from 'node:os';
import type { DskCode } from './i18n';
import { type Logger, NULL_LOGGER, scrub } from './log';

export const MODULE = 'agent-supervisor' as const;

export const OPERATORS_GROUP = 'localai-operators';
export const AGENT_UNIT = 'localai-agent.service';
const SYSTEMCTL = '/usr/bin/systemctl';
const GETENT = '/usr/bin/getent';

/**
 * Why the token file could not be read (it answered `EACCES`):
 * - `not-member`: the user is not in `localai-operators` (LAI-DSK-001);
 * - `needs-relogin`: the user is in the group, but this login session does not have it yet (LAI-DSK-002);
 * - `member-active`: the user is in the group and has it; the group is not the cause: the folder or the file blocks it (LAI-DSK-017).
 */
export type GroupDiagnosis = { kind: 'not-member' | 'needs-relogin' | 'member-active'; gid?: number };
/**
 * EACCES on the token file, by group diagnosis: not in the group (001), in it but not in this login yet (002), or in it
 * and active, so the group is not the cause: the folder or the file blocks it (017, issue #483). "Allow this user" is
 * offered only for 001; for a member it changes nothing.
 */
export function codeForGroupDiagnosis(kind: 'not-member' | 'needs-relogin' | 'member-active'): DskCode {
  if (kind === 'needs-relogin') return 'LAI-DSK-002';
  return kind === 'member-active' ? 'LAI-DSK-017' : 'LAI-DSK-001';
}

export interface GroupDeps {
  /** Output of `getent group localai-operators` (`name:x:gid:member,member`), or null when the group does not exist. */
  getentGroup(): Promise<string | null>;
  username(): string;
  /** The user's primary gid (members whose primary group it is are not listed by `getent`). */
  primaryGid(): number;
  /** `process.getgroups()`: the groups of this process (Node includes the effective gid). */
  processGroups(): number[];
}

function execText(file: string, args: string[], timeoutMs = 3000): Promise<{ code: number | string | null; stdout: string }> {
  return new Promise((resolve) => {
    execFile(file, args, { timeout: timeoutMs, encoding: 'utf8', windowsHide: true }, (error, stdout) => {
      if (!error) return resolve({ code: 0, stdout });
      const e = error as NodeJS.ErrnoException & { code?: number | string };
      resolve({ code: e.code ?? null, stdout: typeof stdout === 'string' ? stdout : '' });
    });
  });
}

export const defaultGroupDeps: GroupDeps = {
  async getentGroup() {
    const result = await execText(GETENT, ['group', OPERATORS_GROUP]);
    return result.code === 0 && result.stdout.trim() !== '' ? result.stdout.trim() : null;
  },
  username: () => userInfo().username,
  primaryGid: () => userInfo().gid,
  processGroups: () => (typeof process.getgroups === 'function' ? process.getgroups() : []),
};

/** The table of desktop.md 4.2, from the groups database and this process's groups. */
export async function diagnoseGroupAccess(deps: GroupDeps = defaultGroupDeps): Promise<GroupDiagnosis> {
  const line = await deps.getentGroup();
  if (line === null) return { kind: 'not-member' };
  const [, , gidText = '', members = ''] = line.split(':');
  const gid = Number(gidText);
  if (!Number.isInteger(gid)) return { kind: 'not-member' };
  const listed = members.split(',').map((m) => m.trim()).includes(deps.username()) || deps.primaryGid() === gid;
  if (!listed) return { kind: 'not-member', gid };
  return deps.processGroups().includes(gid) ? { kind: 'member-active', gid } : { kind: 'needs-relogin', gid };
}

/** `systemctl is-active localai-agent.service`: `active`, `inactive`, `failed`, ... or `unknown` when systemd does not answer. */
export async function unitState(unit: string = AGENT_UNIT, run: typeof execText = execText, systemctl: string = SYSTEMCTL): Promise<string> {
  const result = await run(systemctl, ['is-active', unit]);
  const word = result.stdout.trim().split('\n')[0] ?? '';
  return /^[a-z-]+$/.test(word) ? word : 'unknown';
}

// ---------------------------------------------------------------------------------------------------------------------
// Start agent (desktop.md 4.2)
// ---------------------------------------------------------------------------------------------------------------------

export const PKEXEC = '/usr/bin/pkexec';
/** Environment variables of the test hooks: honoured only by `resolveStartBinaries` (unpackaged builds and the e2e test copy). */
export const SYSTEMCTL_ENV = 'LOCALAI_DESKTOP_SYSTEMCTL';
export const PKEXEC_ENV = 'LOCALAI_DESKTOP_PKEXEC';
/** How long the agent gets to answer /health after the unit was started, and how often it is asked. */
export const HEALTH_WAIT_MS = 30_000;
export const HEALTH_POLL_MS = 1000;
/** `systemctl start` waits for the unit; `pkexec` waits for a person to type a password. */
export const SYSTEMCTL_START_TIMEOUT_MS = 30_000;
export const PKEXEC_TIMEOUT_MS = 120_000;
const STATUS_TIMEOUT_MS = 5000;
const EXCERPT_MAX_CHARS = 4000;

export interface StartBinaries {
  systemctl: string;
  /** Null: no pkexec may be run (a test build whose systemctl is a stub and which names no pkexec stub). */
  pkexec: string | null;
}

/**
 * The binaries "Start agent" runs. The fixed system paths, except in an unpackaged build and in the e2e test copy
 * (`allowOverride`), where `LOCALAI_DESKTOP_SYSTEMCTL` replaces systemctl and `LOCALAI_DESKTOP_PKEXEC` replaces pkexec.
 * With a systemctl override and no pkexec override the real pkexec is never run: a test must not ask a real polkit.
 */
export function resolveStartBinaries(env: NodeJS.ProcessEnv, allowOverride: boolean): StartBinaries {
  const absolute = (value: string | undefined): string | null => (allowOverride && value !== undefined && value.startsWith('/') ? value : null);
  const systemctl = absolute(env[SYSTEMCTL_ENV]);
  const pkexec = absolute(env[PKEXEC_ENV]);
  return { systemctl: systemctl ?? SYSTEMCTL, pkexec: pkexec ?? (systemctl !== null ? null : PKEXEC) };
}

export interface ExecResult {
  /** Exit status, an errno string (`ENOENT`, `EACCES`), or null. */
  code: number | string | null;
  stdout: string;
  stderr: string;
  /** The process was killed because it ran past its timeout. */
  timedOut: boolean;
}

/** The only way "Start agent" touches the system. Production: `execFile`; tests: a fake. */
export type ExecFn = (file: string, args: readonly string[], options: { timeoutMs: number }) => Promise<ExecResult>;

export const execFileRunner: ExecFn = (file, args, options) =>
  new Promise((resolve) => {
    execFile(file, [...args], { timeout: options.timeoutMs, encoding: 'utf8', windowsHide: true, maxBuffer: 1024 * 1024 }, (error, stdout, stderr) => {
      const out = typeof stdout === 'string' ? stdout : '';
      const err = typeof stderr === 'string' ? stderr : '';
      if (!error) return resolve({ code: 0, stdout: out, stderr: err, timedOut: false });
      const e = error as NodeJS.ErrnoException & { code?: number | string; killed?: boolean; signal?: string | null };
      resolve({ code: e.code ?? null, stdout: out, stderr: err, timedOut: e.killed === true && e.signal != null });
    });
  });

/** What `systemctl start` printed when polkit refused the call (or wanted a password that `--no-ask-password` cannot ask). */
const AUTH_ERROR = /interactive authentication required|access denied|authentication (?:is )?required|not authori[sz]ed|failed to (?:start|connect).*(?:permission denied|authentication)/i;

/** True when a failed `systemctl start --no-ask-password` means "ask the administrator", not "the unit is broken". */
export function isAuthorizationError(result: Pick<ExecResult, 'code' | 'stderr' | 'stdout'>): boolean {
  return result.code !== 0 && AUTH_ERROR.test(`${result.stderr}\n${result.stdout}`);
}

/** Defensive scrub of a `systemctl status` excerpt before it is shown: tokens, 64-hex strings, control characters, length. */
export function scrubExcerpt(text: string): string {
  const cleaned = scrub(text)
    .replace(/\b(?:lat|lai|nod|pat|pg)_[A-Za-z0-9_-]+/g, '[redacted]')
    .replace(/\b[0-9a-fA-F]{64}\b/g, '[redacted]')
    // eslint-disable-next-line no-control-regex
    .replace(/\u001b\[[0-9;]*[A-Za-z]/g, '')
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f]/g, '')
    .trim();
  return cleaned.length > EXCERPT_MAX_CHARS ? `${cleaned.slice(0, EXCERPT_MAX_CHARS)}\n...` : cleaned;
}

/** Why a start did not work (logged, never shown: the page shows LAI-DSK-005 and the excerpt). */
export type StartFailure = 'not-authorized' | 'pkexec-failed' | 'pkexec-unavailable' | 'start-failed' | 'timeout';

export type StartAgentResult =
  | { ok: true }
  | { ok: false; code: 'LAI-DSK-005'; statusExcerpt?: string; reason: StartFailure }
  /** Another start is running: this call did nothing (the button keeps showing "Starting..."). */
  | { ok: false; busy: true; code?: undefined };

export interface StartAgentDeps {
  binaries: StartBinaries;
  exec: ExecFn;
  /** One /health probe: true when the agent answered. */
  health(): Promise<boolean>;
  /** `systemctl is-active` answer, or `unknown`. */
  unitState(): Promise<string>;
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
  log?: Logger;
}

const realSleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * The 3 steps of desktop.md 4.2. A unit that is already active (or activating) skips straight to the wait. Any failure
 * that is not an authorization error (unit missing, failed to start) skips pkexec: asking for a password cannot fix it.
 */
export async function startAgent(deps: StartAgentDeps): Promise<Exclude<StartAgentResult, { busy: true }>> {
  const log = deps.log ?? NULL_LOGGER;
  const sleep = deps.sleep ?? realSleep;
  const now = deps.now ?? Date.now;
  const { binaries, exec } = deps;

  const fail = async (reason: StartFailure): Promise<Exclude<StartAgentResult, { busy: true }>> => {
    // Read-only, no privilege: the last lines of `systemctl status`. It exits 3 for a stopped unit; the text is what counts.
    const status = await exec(binaries.systemctl, ['status', AGENT_UNIT, '--no-pager', '-n', '20'], { timeoutMs: STATUS_TIMEOUT_MS });
    const excerpt = scrubExcerpt(status.stdout !== '' ? status.stdout : status.stderr);
    log.log('error', 'agent', `the agent could not be started (${reason})`, { code: 'LAI-DSK-005' });
    return { ok: false, code: 'LAI-DSK-005', reason, ...(excerpt !== '' ? { statusExcerpt: excerpt } : {}) };
  };

  const state = await deps.unitState();
  if (state !== 'active' && state !== 'activating') {
    // 1. Silent when the installer's polkit rule exists.
    const first = await exec(binaries.systemctl, ['start', '--no-ask-password', AGENT_UNIT], { timeoutMs: SYSTEMCTL_START_TIMEOUT_MS });
    if (first.code !== 0) {
      if (first.timedOut || !isAuthorizationError(first)) return fail(first.timedOut ? 'timeout' : 'start-failed');
      // 2. Ask for the administrator password. 126 / 127: dismissed, or not authorised.
      if (binaries.pkexec === null) return fail('pkexec-unavailable');
      log.log('info', 'agent', 'systemctl needs authorization; asking pkexec');
      const second = await exec(binaries.pkexec, [binaries.systemctl, 'start', AGENT_UNIT], { timeoutMs: PKEXEC_TIMEOUT_MS });
      if (second.code !== 0) return fail(second.code === 126 || second.code === 127 ? 'not-authorized' : second.timedOut ? 'timeout' : 'pkexec-failed');
    }
  }

  // 3. The agent answers /health within 30 s (asked at once, then every second).
  const deadline = now() + HEALTH_WAIT_MS;
  for (;;) {
    if (await deps.health()) {
      log.log('info', 'agent', 'the agent is running');
      return { ok: true };
    }
    if (now() >= deadline) return fail('timeout');
    await sleep(HEALTH_POLL_MS);
  }
}

export interface AgentSupervisor {
  unitState(): Promise<string>;
  diagnoseTokenAccess(): Promise<GroupDiagnosis>;
  /** "Start agent". Absent when the supervisor was built without the pieces to start (the error page then answers LAI-DSK-005). One start at a time. */
  start?(): Promise<StartAgentResult>;
}

export interface SupervisorDeps {
  group?: GroupDeps;
  unit?: () => Promise<string>;
  /** Present: the supervisor can start the agent. */
  start?: Omit<StartAgentDeps, 'unitState'> & { unitState?: () => Promise<string> };
}

export function createAgentSupervisor(deps: SupervisorDeps = {}): AgentSupervisor {
  const state = deps.unit ?? (() => unitState(AGENT_UNIT, execText, deps.start?.binaries.systemctl));
  let running = false;
  const supervisor: AgentSupervisor = {
    unitState: state,
    diagnoseTokenAccess: () => diagnoseGroupAccess(deps.group ?? defaultGroupDeps),
  };
  const startDeps = deps.start;
  if (startDeps !== undefined) {
    supervisor.start = async () => {
      if (running) return { ok: false, busy: true };
      running = true;
      try {
        return await startAgent({ ...startDeps, unitState: startDeps.unitState ?? state });
      } finally {
        running = false;
      }
    };
  }
  return supervisor;
}
