// The privileged fixes of the error pages (docs/specs/desktop.md 4.2, "Buttons instead of commands"): "Allow this user"
// (LAI-DSK-001), "Make a new token" (LAI-DSK-007) and "Restart the computer" (the agent-down pages), plus "Open the
// library only" (kiwix-serve on 127.0.0.1:8888, which does not need the agent).
//
// The rules, the same as "Start agent" in agent-supervisor.ts:
// - every call is a FIXED argv from the table below, spawned with `execFile` (never a shell, never a string command);
// - nothing a page sends reaches an argument: the page names an action, the main process picks the argv;
// - the one variable argument, the user name of "Allow this user", comes from the operating system (`os.userInfo()`),
//   not from the page, and is refused unless it is a plain POSIX user name (and never `root`);
// - `pkexec` is the only way to get privilege: the system shows its own password window. No password passes through
//   this app. A build whose test hooks name no pkexec stub never runs a real pkexec (`StartBinaries.pkexec === null`);
// - the log names the action and the outcome, never the argv's user name, never output.
import { userInfo } from 'node:os';
import { type ExecFn, OPERATORS_GROUP, type StartBinaries } from './agent-supervisor';
import { type Logger, NULL_LOGGER } from './log';

export const MODULE = 'system-actions' as const;

/** The binaries the fixed argvs name (overview.md canonical names; usermod is the distribution's). */
export const USERMOD = '/usr/sbin/usermod';
/** The multi-call binary itself, so the fix works even when the `/usr/local/bin/aictl` symlink is missing (aictl.md 3.1). */
export const AIAGENT = '/opt/localai/bin/aiagent';
/** Kiwix-serve, the library program: a separate unit that keeps running when the agent is down (companion-services.md). */
export const LIBRARY_URL = 'http://127.0.0.1:8888/library/';

/** `pkexec` waits for a person to type a password; `usermod` and `token rotate` themselves take well under a second. */
export const PRIVILEGED_TIMEOUT_MS = 120_000;
const REBOOT_TIMEOUT_MS = 30_000;

/** POSIX portable user names as `useradd` accepts them (an optional trailing `$` for machine accounts is refused too). */
const USERNAME = /^[a-z_][a-z0-9_-]{0,31}$/;

export function isSafeUsername(name: unknown): name is string {
  return typeof name === 'string' && USERNAME.test(name) && name !== 'root';
}

export interface Argv {
  file: string;
  args: readonly string[];
}

/** `pkexec /usr/sbin/usermod -aG localai-operators <user>`. Null when the name is not a plain user name. */
export function allowUserArgv(pkexec: string, username: string): Argv | null {
  if (!isSafeUsername(username)) return null;
  return { file: pkexec, args: [USERMOD, '-aG', OPERATORS_GROUP, username] };
}

/** `pkexec /opt/localai/bin/aiagent ctl token rotate` (aictl.md 4.21, a [local] [root] command: no agent needed). */
export function rotateTokenArgv(pkexec: string): Argv {
  return { file: pkexec, args: [AIAGENT, 'ctl', 'token', 'rotate'] };
}

/**
 * `systemctl reboot`, without pkexec: logind and polkit decide. The person at the keyboard of the active local session
 * may restart without a password (polkit's default for `org.freedesktop.login1.reboot`); when other people are logged
 * in, the system's own password window asks. This is the least privilege that works.
 */
export function rebootArgv(systemctl: string): Argv {
  return { file: systemctl, args: ['reboot'] };
}

/** Why a fix did not work. `cancelled`: the password window was closed or the password was refused (pkexec 126/127). */
export type SystemFailure = 'cancelled' | 'failed' | 'unavailable' | 'invalid-user' | 'busy';

export type SystemResult = { ok: true } | { ok: false; reason: SystemFailure };

export interface SystemActions {
  allowUser(): Promise<SystemResult>;
  rotateToken(): Promise<SystemResult>;
  reboot(): Promise<SystemResult>;
}

export interface SystemActionDeps {
  binaries: StartBinaries;
  exec: ExecFn;
  /** The desktop user's login name (production: `os.userInfo().username`). */
  username?: () => string;
  log?: Logger;
}

/** One privileged call at a time: a second press while the password window is open does nothing. */
export function createSystemActions(deps: SystemActionDeps): SystemActions {
  const log = deps.log ?? NULL_LOGGER;
  const username = deps.username ?? (() => userInfo().username);
  let running = false;

  const run = async (name: string, argv: Argv | null, timeoutMs: number): Promise<SystemResult> => {
    if (running) return { ok: false, reason: 'busy' };
    if (argv === null) {
      log.log('warn', 'app', `${name}: refused, the user name is not a plain POSIX name`);
      return { ok: false, reason: 'invalid-user' };
    }
    running = true;
    try {
      const result = await deps.exec(argv.file, argv.args, { timeoutMs });
      if (result.code === 0) {
        log.log('info', 'app', `${name}: done`);
        return { ok: true };
      }
      const reason: SystemFailure = result.code === 126 || result.code === 127 ? 'cancelled' : result.code === 'ENOENT' ? 'unavailable' : 'failed';
      log.log('warn', 'app', `${name}: did not work (${reason})`, { exit: typeof result.code === 'number' ? result.code : String(result.code), timedOut: result.timedOut });
      return { ok: false, reason };
    } finally {
      running = false;
    }
  };

  const pkexec = deps.binaries.pkexec;
  return {
    allowUser: () => {
      if (pkexec === null) return Promise.resolve({ ok: false, reason: 'unavailable' });
      return run('allow this user', allowUserArgv(pkexec, username()), PRIVILEGED_TIMEOUT_MS);
    },
    rotateToken: () => {
      if (pkexec === null) return Promise.resolve({ ok: false, reason: 'unavailable' });
      return run('make a new token', rotateTokenArgv(pkexec), PRIVILEGED_TIMEOUT_MS);
    },
    reboot: () => run('restart the computer', rebootArgv(deps.binaries.systemctl), REBOOT_TIMEOUT_MS),
  };
}
