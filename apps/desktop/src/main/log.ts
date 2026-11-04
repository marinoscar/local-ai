// Main-process log: JSON lines in ~/.config/ai-desktop/logs/main.log, rotated at 5 MB, three files (docs/specs/desktop.md 3.8, 7).
// Never tokens, keys or PINs: every line passes through `scrub()` before it is written, and callers log codes and
// host names, not credentials or paths that hold them.
import { appendFileSync, existsSync, mkdirSync, renameSync, rmSync, statSync } from 'node:fs';
import { join } from 'node:path';

export const MODULE = 'log' as const;

export type Level = 'debug' | 'info' | 'warn' | 'error';
export type Component = 'app' | 'agent' | 'tray' | 'egress' | 'cloud' | 'updates' | 'survival';

export interface LogEntry {
  ts: string;
  level: Level;
  msg: string;
  component: Component;
  code?: string;
  [field: string]: unknown;
}

export interface Logger {
  /** Writes one entry as it is (the egress guard hands over its own shape). */
  write(entry: LogEntry): void;
  log(level: Level, component: Component, msg: string, fields?: Record<string, unknown>): void;
}

export const DEFAULT_MAX_BYTES = 5 * 1024 * 1024;
/** `main.log`, `main.log.1`, `main.log.2`: three files at most, so 15 MB at most on disk. */
export const DEFAULT_FILES = 3;

const SECRET_PATTERNS: ReadonlyArray<readonly [RegExp, string]> = [
  [/\b(?:lat|lai|nod|pat|pg)_[A-Za-z0-9_-]{16,}/g, '[redacted]'],
  [/\bBearer\s+\S+/gi, 'Bearer [redacted]'],
  [/(localai_session|lai_portal)=[^\s;"']+/g, '$1=[redacted]'],
];

/** Removes anything that looks like a token, a bearer header or a session cookie. */
export function scrub(text: string): string {
  let out = text;
  for (const [pattern, replacement] of SECRET_PATTERNS) out = out.replace(pattern, replacement);
  return out;
}

export interface FileLoggerOptions {
  dir: string;
  maxBytes?: number;
  files?: number;
  now?: () => Date;
  /** Also print each line on stderr (default true: a terminal launch and the e2e egress check read it). */
  stderr?: boolean;
}

export const NULL_LOGGER: Logger = { write: () => undefined, log: () => undefined };

export function createFileLogger(options: FileLoggerOptions): Logger {
  const maxBytes = options.maxBytes ?? DEFAULT_MAX_BYTES;
  const files = Math.max(1, options.files ?? DEFAULT_FILES);
  const now = options.now ?? (() => new Date());
  const path = join(options.dir, 'main.log');
  let size = -1;

  const currentSize = (): number => {
    if (size < 0) {
      try {
        size = statSync(path).size;
      } catch {
        size = 0;
      }
    }
    return size;
  };

  const rotate = (): void => {
    // main.log.(files-1) is dropped, the others move up one, main.log becomes main.log.1.
    if (files === 1) {
      rmSync(path, { force: true });
    } else {
      rmSync(`${path}.${files - 1}`, { force: true });
      for (let i = files - 2; i >= 1; i -= 1) {
        if (existsSync(`${path}.${i}`)) renameSync(`${path}.${i}`, `${path}.${i + 1}`);
      }
      if (existsSync(path)) renameSync(path, `${path}.1`);
    }
    size = 0;
  };

  const write = (entry: LogEntry): void => {
    try {
      const line = `${scrub(JSON.stringify(entry))}\n`;
      if (options.stderr !== false) process.stderr.write(line);
      mkdirSync(options.dir, { recursive: true, mode: 0o700 });
      const bytes = Buffer.byteLength(line);
      if (currentSize() > 0 && currentSize() + bytes > maxBytes) rotate();
      appendFileSync(path, line, { mode: 0o600 });
      size = currentSize() + bytes;
    } catch {
      // A broken log must never break the app.
    }
  };

  return {
    write,
    log: (level, component, msg, fields = {}) => write({ ...fields, ts: now().toISOString(), level, msg, component }),
  };
}

/** `{ ts, level, msg, component, code? }` for a plain message; `code` is a doctor code when there is one. */
export function logWith(logger: Logger, level: Level, component: Component, msg: string, code?: string, fields: Record<string, unknown> = {}): void {
  logger.log(level, component, msg, code === undefined ? fields : { ...fields, code });
}
