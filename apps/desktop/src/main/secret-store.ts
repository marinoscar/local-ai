// Secret store: the lab PAT, encrypted with Electron's safeStorage (docs/specs/desktop.md 4.8, 3.8).
//
//   - The token goes to `~/.config/ai-desktop/cloud-token.bin` (0600, in a 0700 folder) as `safeStorage.encryptString(json)`.
//   - On a desktop with no password keyring (`getSelectedStorageBackend() === 'basic_text'`) the key protecting the file
//     is weak, so the user must consent first ("Store it anyway?"). The consent is `settings.secrets.allowPlaintext`.
//     Without it the token is kept in memory for this run only (LAI-DSK-008) and no file is written.
//   - A file that cannot be decrypted (the keyring changed) is `unreadable`: the caller treats the desktop as unlinked
//     and shows LAI-DSK-011.
// This module never logs a token and never returns one to anything but `cloud-link.ts`. The module imports only types
// from `electron`, so it loads under Vitest.
import { chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { NULL_LOGGER, type Logger } from './log';

export const MODULE = 'secret-store' as const;

/** The file name inside the config folder. */
export const TOKEN_FILE = 'cloud-token.bin';
export const NO_KEYRING_CODE = 'LAI-DSK-008' as const;

export function tokenFilePath(configDir: string): string {
  return join(configDir, TOKEN_FILE);
}

/** The slice of Electron's `safeStorage` this module uses. */
export interface SafeStorageLike {
  isEncryptionAvailable(): boolean;
  /** Linux only; absent elsewhere. */
  getSelectedStorageBackend?(): string;
  encryptString(plainText: string): Buffer;
  decryptString(encrypted: Buffer): string;
  /** Linux only: use the in-memory key when no password manager exists (after the user consented). */
  setUsePlainTextEncryption?(usePlainText: boolean): void;
}

/** `strong`: a password manager protects the key. `weak`: none does (basic_text), or the backend is not known yet. */
export type Protection = 'strong' | 'weak';

export function detectProtection(safeStorage: SafeStorageLike): Protection {
  let backend: string | undefined;
  try {
    backend = safeStorage.getSelectedStorageBackend?.();
  } catch {
    backend = undefined;
  }
  if (backend === 'basic_text' || backend === 'unknown') return 'weak';
  // Another platform has no backend name; encryption must then simply be available.
  if (backend === undefined) return safeStorage.isEncryptionAvailable() ? 'strong' : 'weak';
  return 'strong';
}

/** What is kept: the token, its id (to revoke it) and when it expires. */
export interface StoredSecret {
  token: string;
  tokenId: string;
  expiresAt: string;
}

export type SaveResult =
  | { where: 'file' }
  /** Not written to disk: `no-keyring` is LAI-DSK-008 (consent refused), `unavailable` is any other reason. */
  | { where: 'memory'; reason: 'no-keyring' | 'unavailable' };

export type LoadResult =
  | { kind: 'none' }
  | { kind: 'ok'; secret: StoredSecret }
  /** A file exists but cannot be used (damaged, or encrypted with a key that is gone). */
  | { kind: 'unreadable' };

export interface SecretStoreDeps {
  /** Absolute path of `cloud-token.bin`. */
  file: string;
  safeStorage: SafeStorageLike;
  /** `settings.secrets.allowPlaintext`. */
  allowPlaintext(): boolean;
  /** Remembers the consent (`settings.secrets.allowPlaintext = true`). */
  rememberConsent(): Promise<void>;
  /** Shows "This computer has no password keyring ... Store it anyway?". True when the user agreed. */
  askConsent(): Promise<boolean>;
  log?: Logger;
}

export interface SecretStore {
  save(secret: StoredSecret): Promise<SaveResult>;
  load(): Promise<LoadResult>;
  /** Removes the file. A missing file is fine. */
  clear(): Promise<void>;
  protection(): Protection;
}

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

function parseSecret(text: string): StoredSecret | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return null;
  }
  if (!isObject(parsed) || parsed['v'] !== 1) return null;
  const { token, tokenId, expiresAt } = parsed;
  if (typeof token !== 'string' || token === '' || typeof tokenId !== 'string' || typeof expiresAt !== 'string') return null;
  return { token, tokenId, expiresAt };
}

export function createSecretStore(deps: SecretStoreDeps): SecretStore {
  const log = deps.log ?? NULL_LOGGER;
  const { safeStorage } = deps;
  const protection = (): Protection => detectProtection(safeStorage);

  /** After the user consented, the in-memory key is what lets `encryptString` work on a desktop with no keyring. */
  const usePlainTextKey = (): void => {
    try {
      safeStorage.setUsePlainTextEncryption?.(true);
    } catch {
      // not Linux, or too old: encryptString decides
    }
  };

  async function writeFileAtomic(bytes: Buffer): Promise<void> {
    const dir = dirname(deps.file);
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const temporary = `${deps.file}.${process.pid}.tmp`;
    await writeFile(temporary, bytes, { mode: 0o600 });
    await chmod(temporary, 0o600);
    await rename(temporary, deps.file);
  }

  return {
    protection,

    async save(secret) {
      if (protection() === 'weak') {
        if (!deps.allowPlaintext()) {
          const agreed = await deps.askConsent();
          if (!agreed) {
            log.log('warn', 'cloud', 'no password keyring and no consent: the lab key is kept in memory only', { code: NO_KEYRING_CODE });
            return { where: 'memory', reason: 'no-keyring' };
          }
          await deps.rememberConsent();
        }
        usePlainTextKey();
      }
      let encrypted: Buffer;
      try {
        encrypted = safeStorage.encryptString(JSON.stringify({ v: 1, ...secret }));
      } catch {
        log.log('warn', 'cloud', 'the lab key could not be encrypted: kept in memory only');
        return { where: 'memory', reason: 'unavailable' };
      }
      try {
        await writeFileAtomic(encrypted);
      } catch {
        log.log('warn', 'cloud', 'the lab key file could not be written: kept in memory only');
        return { where: 'memory', reason: 'unavailable' };
      }
      return { where: 'file' };
    },

    async load() {
      let bytes: Buffer;
      try {
        bytes = await readFile(deps.file);
      } catch (error) {
        return (error as NodeJS.ErrnoException).code === 'ENOENT' ? { kind: 'none' } : { kind: 'unreadable' };
      }
      // A file written with the consent needs the same in-memory key to be read back.
      if (protection() === 'weak' && deps.allowPlaintext()) usePlainTextKey();
      try {
        const secret = parseSecret(safeStorage.decryptString(bytes));
        return secret ? { kind: 'ok', secret } : { kind: 'unreadable' };
      } catch {
        return { kind: 'unreadable' };
      }
    },

    async clear() {
      await rm(deps.file, { force: true });
    },
  };
}
