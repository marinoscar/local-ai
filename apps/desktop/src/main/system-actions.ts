// The privileged fixes of the error pages (docs/specs/desktop.md 4.2, "Buttons instead of commands"): "Allow this user"
// (LAI-DSK-001), "Fix access" (LAI-DSK-017), "Make a new token" (LAI-DSK-007) and "Restart the computer" (the agent-down pages), plus "Open the
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
//
// Issue #500 adds the System screen's buttons (dashboard.md 3.2): the agent page names one of SYSTEM_ACTION_NAMES through
// `window.localaiDesktopHost.runSystemAction(name)`, and `systemActionArgv` maps it to a fixed `pkexec aiagent ctl …`
// argv. A name outside the list is refused before anything runs.
import { userInfo } from 'node:os';
import type { SystemActionName, SystemActionResult } from '../shared/types';
import { type ExecFn, type ExecResult, OPERATORS_GROUP, type StartBinaries } from './agent-supervisor';
import { type Logger, NULL_LOGGER } from './log';

export const MODULE = 'system-actions' as const;

/** The binaries the fixed argvs name (overview.md canonical names; usermod is the distribution's). */
export const USERMOD = '/usr/sbin/usermod';
/** The distribution's chmod (Ubuntu's /bin is a link to /usr/bin). */
export const CHMOD = '/usr/bin/chmod';
/** The configuration folder that holds agent-token (overview.md canonical names). */
export const ETC_DIR = '/etc/localai';
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

/**
 * `pkexec /usr/bin/chmod o+x /etc/localai` (issue #483): the pass-through bit only, so an operator reaches agent-token
 * (root:localai-operators 0640) without being able to list the folder. A direct chmod rather than `aiagent setup dirs`,
 * because an agent older than the fix would set the folder back to 0750.
 */
export function repairAccessArgv(pkexec: string): Argv {
  return { file: pkexec, args: [CHMOD, 'o+x', ETC_DIR] };
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

/** The System screen's privileged buttons (dashboard.md 3.2), in the order of the screen. Nothing else can be named. */
export const SYSTEM_ACTION_NAMES = ['update-agent', 'kit-verify', 'backup-config', 'restore-config'] as const satisfies readonly SystemActionName[];

export function isSystemActionName(name: unknown): name is SystemActionName {
  return typeof name === 'string' && (SYSTEM_ACTION_NAMES as readonly string[]).includes(name);
}

/** How long each one may take, password window included: `update` itself waits up to 15 min (aictl.md 4.27). */
export const SYSTEM_ACTION_TIMEOUT_MS: Record<SystemActionName, number> = {
  'update-agent': 20 * 60_000,
  'kit-verify': 60 * 60_000,
  'backup-config': PRIVILEGED_TIMEOUT_MS,
  'restore-config': 5 * 60_000,
};

/**
 * The fixed argv of a System-screen button. Global flags come before the command name (aictl.md 3.1): `--json` so the
 * answer (or the error envelope with its doctor code) can be read, and `--lang` in the app's language. `kit verify` is
 * read by its exit code instead (its JSON report lists every file of the kit and may be large).
 *  - update-agent:   pkexec aiagent ctl --json --lang <l> update --yes                       (aictl.md 4.27)
 *  - kit-verify:     pkexec aiagent ctl --lang <l> kit verify                                (aictl.md 4.23c)
 *  - backup-config:  pkexec aiagent ctl --json --lang <l> backup-config --to-ark --yes      (aictl.md 4.25)
 *  - restore-config: pkexec aiagent ctl --json --lang <l> backup-config --restore latest --yes
 */
export function systemActionArgv(pkexec: string, name: SystemActionName, lang: 'en' | 'es'): Argv {
  const ctl = [AIAGENT, 'ctl'];
  switch (name) {
    case 'update-agent':
      return { file: pkexec, args: [...ctl, '--json', '--lang', lang, 'update', '--yes'] };
    case 'kit-verify':
      return { file: pkexec, args: [...ctl, '--lang', lang, 'kit', 'verify'] };
    case 'backup-config':
      return { file: pkexec, args: [...ctl, '--json', '--lang', lang, 'backup-config', '--to-ark', '--yes'] };
    case 'restore-config':
      return { file: pkexec, args: [...ctl, '--json', '--lang', lang, 'backup-config', '--restore', 'latest', '--yes'] };
  }
}

const DOCTOR_CODE = /\bLAI-[A-Z]{2,4}-\d{3}\b/;

/** The last JSON object the command printed on stdout (with `--json` it prints exactly one), or null. */
function lastDocument(stdout: string): Record<string, unknown> | null {
  const text = stdout.trim();
  if (text === '') return null;
  try {
    const value: unknown = JSON.parse(text);
    return typeof value === 'object' && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);
const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);

/**
 * interpretSystemAction turns what the command did into the page's answer: never the raw output, only the fields the
 * screen words its sentence from (a version, a count, the kit's colour) or the error envelope's code, message and fix.
 */
export function interpretSystemAction(name: SystemActionName, r: ExecResult): SystemActionResult {
  if (r.code === 'ENOENT') return { ok: false, reason: 'unavailable' };
  if (r.code === 126 || r.code === 127) return { ok: false, reason: 'cancelled' };
  if (r.timedOut) return { ok: false, reason: 'timeout' };
  if (name === 'kit-verify') {
    // aictl.md 4.23c: 0 all fine, 8 warnings only, 9 parts missing or inconsistent, 1 a damaged file or a bad signature.
    const overall = r.code === 0 ? 'green' : r.code === 8 ? 'yellow' : r.code === 9 || r.code === 1 ? 'red' : null;
    const code = (`${r.stdout}\n${r.stderr}`.match(DOCTOR_CODE) ?? [])[0];
    if (overall !== null) return { ok: true, overall, ...(overall !== 'green' && code ? { code } : {}) };
    return { ok: false, reason: 'failed', ...(code ? { code } : {}) };
  }
  const doc = lastDocument(r.stdout);
  const err = doc && typeof doc.error === 'object' && doc.error !== null ? (doc.error as Record<string, unknown>) : null;
  if (r.code !== 0 || err) {
    const code = str(err?.code) ?? (`${r.stdout}\n${r.stderr}`.match(DOCTOR_CODE) ?? [])[0];
    const message = str(err?.message);
    const fix = str(err?.fix);
    return { ok: false, reason: 'failed', ...(code ? { code } : {}), ...(message ? { message } : {}), ...(fix ? { fix } : {}) };
  }
  if (!doc) return { ok: true };
  switch (name) {
    case 'update-agent': {
      const status = str(doc.status);
      const from = str(doc.from);
      const to = str(doc.to);
      return { ok: true, ...(status ? { status } : {}), ...(from ? { from } : {}), ...(to ? { to } : {}) };
    }
    case 'backup-config': {
      const files = num(doc.files);
      return { ok: true, ...(files !== undefined ? { files } : {}), ...(typeof doc.secrets_included === 'boolean' ? { secretsIncluded: doc.secrets_included } : {}) };
    }
    case 'restore-config': {
      const restored = num(doc.restored);
      return { ok: true, ...(restored !== undefined ? { restored } : {}), ...(typeof doc.restarted === 'boolean' ? { restarted: doc.restarted } : {}) };
    }
  }
}

export interface SystemActions {
  allowUser(): Promise<SystemResult>;
  repairAccess(): Promise<SystemResult>;
  rotateToken(): Promise<SystemResult>;
  reboot(): Promise<SystemResult>;
  /** A System-screen button (issue #500). `name` comes from a page: anything outside SYSTEM_ACTION_NAMES runs nothing. */
  runNamed(name: unknown, lang: 'en' | 'es'): Promise<SystemActionResult>;
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
    repairAccess: () => {
      if (pkexec === null) return Promise.resolve({ ok: false, reason: 'unavailable' });
      return run('fix access', repairAccessArgv(pkexec), PRIVILEGED_TIMEOUT_MS);
    },
    rotateToken: () => {
      if (pkexec === null) return Promise.resolve({ ok: false, reason: 'unavailable' });
      return run('make a new token', rotateTokenArgv(pkexec), PRIVILEGED_TIMEOUT_MS);
    },
    reboot: () => run('restart the computer', rebootArgv(deps.binaries.systemctl), REBOOT_TIMEOUT_MS),
    runNamed: async (name, lang) => {
      if (!isSystemActionName(name)) {
        log.log('warn', 'app', 'a system action was refused: not on the list');
        return { ok: false, reason: 'unknown-action' };
      }
      if (pkexec === null) return { ok: false, reason: 'unavailable' };
      if (running) return { ok: false, reason: 'busy' };
      running = true;
      try {
        const argv = systemActionArgv(pkexec, name, lang === 'es' ? 'es' : 'en');
        const answer = interpretSystemAction(name, await deps.exec(argv.file, argv.args, { timeoutMs: SYSTEM_ACTION_TIMEOUT_MS[name] }));
        if (answer.ok) log.log('info', 'app', `${name}: done`);
        else log.log('warn', 'app', `${name}: did not work (${answer.reason})`, answer.code ? { code: answer.code } : undefined);
        return answer;
      } finally {
        running = false;
      }
    },
  };
}
