// Autostart entry at ~/.config/autostart/ai-desktop.desktop (docs/specs/desktop.md 4.5).
// After a power cut and a reboot the ark has to come back by itself: this file makes the desktop session start
// `ai-desktop --hidden` at login. It is the only file this module ever writes or removes; every other file in the
// folder (the user's other autostart entries) is never read, edited or deleted.
// No Electron import: the file system, the environment and the callbacks are injected, so it is unit-tested with a temp dir.
import * as nodeFsPromises from 'node:fs/promises';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { type Logger, NULL_LOGGER } from './log';

export const MODULE = 'autostart' as const;

export const AUTOSTART_FILE_NAME = 'ai-desktop.desktop' as const;
/** Where the deb installs the launcher. */
export const DEB_EXEC_PATH = '/usr/bin/ai-desktop' as const;
export const AUTOSTART_CODE = 'LAI-DSK-014' as const;

/** `$XDG_CONFIG_HOME/autostart` (an absolute path only), else `~/.config/autostart`: the folder the desktop session reads. */
export function autostartDir(env: NodeJS.ProcessEnv = process.env, home: string = homedir()): string {
  const base = env['XDG_CONFIG_HOME'] && env['XDG_CONFIG_HOME'].startsWith('/') ? env['XDG_CONFIG_HOME'] : join(home, '.config');
  return join(base, 'autostart');
}

export function autostartPath(env: NodeJS.ProcessEnv = process.env, home: string = homedir()): string {
  return join(autostartDir(env, home), AUTOSTART_FILE_NAME);
}

// -- the file -----------------------------------------------------------------------------------------------------------

/** Characters that make a Desktop Entry `Exec` argument need quotes (Desktop Entry Specification, "The Exec key"). */
const EXEC_RESERVED = /[\s"'\\><~|&;$*?#()`]/;
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f]/;

/**
 * One `Exec` argument as it is written in the file. `%` is doubled (a field code otherwise); an argument with a space or
 * another reserved character is wrapped in double quotes, with `"`, `` ` ``, `$` and `\` escaped by a backslash; and
 * since the whole value is a Desktop Entry string, every backslash is then doubled once more.
 */
export function quoteExecArg(arg: string): string {
  const percent = arg.replace(/%/g, '%%');
  if (!EXEC_RESERVED.test(arg)) return percent;
  const quoted = `"${percent.replace(/(["`$\\])/g, '\\$1')}"`;
  return quoted.replace(/\\/g, '\\\\');
}

/**
 * The program the entry starts: the AppImage's own absolute path (`$APPIMAGE`, set by the AppImage runtime, so an
 * AppImage that moved fixes itself on the next start) or the deb's launcher. A relative or control-character value
 * is not trusted.
 */
export function resolveExecPath(env: NodeJS.ProcessEnv = process.env): string {
  const appImage = env['APPIMAGE'];
  return appImage !== undefined && appImage.startsWith('/') && !CONTROL_CHARACTERS.test(appImage) ? appImage : DEB_EXEC_PATH;
}

export function execLine(execPath: string): string {
  return `${quoteExecArg(execPath)} --hidden`;
}

/** The exact content of the entry (the deb form is the one of the spec, byte for byte). */
export function desktopEntry(execPath: string): string {
  return [
    '[Desktop Entry]',
    'Type=Application',
    'Name=Local AI',
    `Exec=${execLine(execPath)}`,
    'Icon=ai-desktop',
    'X-GNOME-Autostart-enabled=true',
    'X-GNOME-Autostart-Delay=5',
    'NoDisplay=false',
    '',
  ].join('\n');
}

// -- applying it ----------------------------------------------------------------------------------------------------------

/** The part of `node:fs/promises` that is used. */
export interface AutostartFs {
  mkdir(path: string, options: { recursive: true; mode: number }): Promise<unknown>;
  readFile(path: string, encoding: 'utf8'): Promise<string>;
  writeFile(path: string, data: string, options: { mode: number }): Promise<void>;
  chmod(path: string, mode: number): Promise<void>;
  rename(from: string, to: string): Promise<void>;
  rm(path: string, options: { force: true }): Promise<void>;
}

export const nodeFs: AutostartFs = nodeFsPromises;

export interface ApplyDeps {
  env?: NodeJS.ProcessEnv;
  home?: string;
  fs?: AutostartFs;
  log?: Logger;
}

export type ApplySuccess = { ok: true; action: 'written' | 'unchanged' | 'removed' | 'absent' | 'skipped'; path: string };
export interface ApplyFailure {
  ok: false;
  /** LAI-DSK-014: the toast shows `path`. */
  code: typeof AUTOSTART_CODE;
  path: string;
  /** The errno (`EACCES`, `EROFS`, ...) or the error message. Never user content. */
  reason: string;
}
export type ApplyResult = ApplySuccess | ApplyFailure;

const reasonOf = (error: unknown): string => (error as NodeJS.ErrnoException).code ?? (error as Error).message ?? 'unknown error';

/**
 * Writes the entry (enabled) or removes it (disabled). Atomic: a temporary file next to it, mode 0644, then a rename, so a
 * power cut never leaves half a file; the folder is created when missing. An entry that already has the right content is
 * left alone (a read-only folder with a correct file is not an error). Never throws: a failure is `{ ok: false }`.
 */
export async function applyAutostart(enabled: boolean, deps: ApplyDeps = {}): Promise<ApplyResult> {
  const env = deps.env ?? process.env;
  const fs = deps.fs ?? nodeFs;
  const log = deps.log ?? NULL_LOGGER;
  const path = autostartPath(env, deps.home);
  const fail = (error: unknown): ApplyFailure => {
    const reason = reasonOf(error);
    log.log('warn', 'app', `could not ${enabled ? 'write' : 'remove'} the autostart entry (${reason})`, { code: AUTOSTART_CODE });
    return { ok: false, code: AUTOSTART_CODE, path, reason };
  };

  if (!enabled) {
    const existed = await fs.readFile(path, 'utf8').then(
      () => true,
      () => false,
    );
    try {
      await fs.rm(path, { force: true });
    } catch (error) {
      return fail(error);
    }
    return { ok: true, action: existed ? 'removed' : 'absent', path };
  }

  const execPath = resolveExecPath(env);
  if (CONTROL_CHARACTERS.test(execPath)) return fail(new Error('the program path has control characters'));
  const content = desktopEntry(execPath);
  const current = await fs.readFile(path, 'utf8').catch(() => null);
  if (current === content) return { ok: true, action: 'unchanged', path };

  const temporary = `${path}.${process.pid}.tmp`;
  try {
    await fs.mkdir(dirname(path), { recursive: true, mode: 0o755 });
    await fs.writeFile(temporary, content, { mode: 0o644 });
    // The umask may have narrowed the mode: the entry is 0644 whatever it is.
    await fs.chmod(temporary, 0o644);
    await fs.rename(temporary, path);
  } catch (error) {
    await fs.rm(temporary, { force: true }).catch(() => undefined);
    return fail(error);
  }
  return { ok: true, action: 'written', path };
}

// -- the setting and the menu items ---------------------------------------------------------------------------------------

export interface AutostartControllerDeps {
  /** `settings.autostart` as read at start-up (true on a first run). */
  wanted: boolean;
  apply(enabled: boolean): Promise<ApplyResult>;
  /** Writes `settings.autostart`. */
  persist(enabled: boolean): Promise<void>;
  /** The LAI-DSK-014 toast (with the path). */
  onFailure(failure: ApplyFailure, wanted: boolean): void;
  log?: Logger;
}

/**
 * Owns the "Start at login" state for the tray item (and the top bar menu of DSK-04): `enabled` is what the file system
 * really says after the last attempt, while `settings.autostart` keeps what the user asked for, so a start-up that could
 * not write the file tries again at the next start. Calls are serialised.
 */
export class AutostartController {
  readonly #d: AutostartControllerDeps;
  readonly #log: Logger;
  #enabled: boolean;
  #queue: Promise<unknown> = Promise.resolve();

  constructor(deps: AutostartControllerDeps) {
    this.#d = deps;
    this.#log = deps.log ?? NULL_LOGGER;
    this.#enabled = deps.wanted;
  }

  /** What the tray item shows. */
  get enabled(): boolean {
    return this.#enabled;
  }

  /** Start-up: re-applies the setting, which also repairs an `Exec` line after an AppImage moved. */
  applyAtStart(): Promise<ApplyResult> {
    return this.#run(async () => {
      const wanted = this.#d.wanted;
      const result = await this.#d.apply(wanted);
      this.#enabled = wanted && result.ok;
      if (!result.ok) this.#d.onFailure(result, wanted);
      return result;
    });
  }

  /** The user's choice. Turning it on is only remembered when the file was written; turning it off always is. */
  set(next: boolean): Promise<ApplyResult> {
    return this.#run(() => this.#applyAndPersist(next));
  }

  async #applyAndPersist(next: boolean): Promise<ApplyResult> {
    const result = await this.#d.apply(next);
    this.#enabled = next && result.ok;
    if (!result.ok) this.#d.onFailure(result, next);
    if (result.ok || !next) await this.#persist(next);
    return result;
  }

  /**
   * The setting changed from somewhere else (a settings page, the top bar menu): brings the file in line. A change this
   * controller made itself already matches `enabled` and does nothing, so persisting never loops back here.
   */
  follow(value: boolean): Promise<ApplyResult | null> {
    return this.#run(async () => (value === this.#enabled ? null : this.#applyAndPersist(value)));
  }

  async #persist(value: boolean): Promise<void> {
    try {
      await this.#d.persist(value);
    } catch (error) {
      this.#log.log('warn', 'app', `settings.json could not be written (${reasonOf(error)}); "Start at login" is not remembered`);
    }
  }

  #run<T>(work: () => Promise<T>): Promise<T> {
    const next = this.#queue.then(work, work);
    this.#queue = next.catch(() => undefined);
    return next;
  }
}
