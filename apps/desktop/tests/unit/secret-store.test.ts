// The secret store (docs/specs/desktop.md 4.8 "Token storage"): safeStorage encryption, the no-keyring consent, and a
// file that cannot be read. A fake safeStorage stands in for Electron's; the files are real, in a temporary folder.
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { type SafeStorageLike, type StoredSecret, createSecretStore, detectProtection, tokenFilePath } from '../../src/main/secret-store';

let dir: string;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'ai-desktop-secret-'));
});
afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
});

const sample = (): StoredSecret => ({ token: `pat_${randomBytes(24).toString('base64url')}`, tokenId: randomUUID(), expiresAt: '2027-02-03T12:00:00.000Z' });

/** "Encryption" that is easy to see through in a test: a marker plus reversed bytes, and a key that can be lost. */
function fakeSafeStorage(backend: string | undefined, options: { keyLost?: boolean } = {}): SafeStorageLike & { plain: ReturnType<typeof vi.fn>; keyLost: boolean } {
  const state = { keyLost: options.keyLost ?? false, plainText: false };
  const plain = vi.fn((on: boolean) => void (state.plainText = on));
  const weak = backend === 'basic_text' || backend === 'unknown';
  return {
    plain,
    get keyLost() {
      return state.keyLost;
    },
    set keyLost(value: boolean) {
      state.keyLost = value;
    },
    isEncryptionAvailable: () => !weak || state.plainText,
    ...(backend === undefined ? {} : { getSelectedStorageBackend: () => backend }),
    setUsePlainTextEncryption: plain,
    encryptString: (text) => {
      if (weak && !state.plainText) throw new Error('Encryption is not available.');
      return Buffer.concat([Buffer.from('ENC1'), Buffer.from(text, 'utf8').reverse()]);
    },
    decryptString: (bytes) => {
      if (state.keyLost || bytes.subarray(0, 4).toString() !== 'ENC1') throw new Error('Error while decrypting the ciphertext provided to safeStorage.decryptString.');
      return Buffer.from(bytes.subarray(4)).reverse().toString('utf8');
    },
  };
}

interface Rig {
  store: ReturnType<typeof createSecretStore>;
  safe: ReturnType<typeof fakeSafeStorage>;
  consent: { allowed: boolean; asked: number; answer: boolean; remembered: number };
}

function rig(backend: string | undefined, consent: Partial<Rig['consent']> = {}): Rig {
  const safe = fakeSafeStorage(backend);
  const c = { allowed: false, asked: 0, answer: false, remembered: 0, ...consent };
  const store = createSecretStore({
    file: tokenFilePath(dir),
    safeStorage: safe,
    allowPlaintext: () => c.allowed,
    rememberConsent: async () => void ((c.allowed = true), (c.remembered += 1)),
    askConsent: async () => (c.asked++, c.answer),
  });
  return { store, safe, consent: c };
}

const mode = async (): Promise<number> => (await stat(tokenFilePath(dir))).mode & 0o777;

describe('detectProtection', () => {
  it.each([
    ['gnome_libsecret', undefined, 'strong'],
    ['kwallet6', undefined, 'strong'],
    ['basic_text', undefined, 'weak'],
    ['unknown', undefined, 'weak'],
    [undefined, 'available', 'strong'], // another platform: no backend name, encryption works
  ])('backend %s -> %s', (backend, _x, expected) => {
    expect(detectProtection(fakeSafeStorage(backend))).toBe(expected);
  });
});

describe('save and load', () => {
  it('a keyring backend: the file is written at once, 0600, encrypted, and read back', async () => {
    const { store, consent } = rig('gnome_libsecret');
    const secret = sample();
    expect(await store.save(secret)).toEqual({ where: 'file' });
    expect(consent.asked).toBe(0);
    expect(await mode()).toBe(0o600);
    const bytes = await readFile(tokenFilePath(dir));
    expect(bytes.toString('utf8')).not.toContain(secret.token); // not in the clear
    expect(await store.load()).toEqual({ kind: 'ok', secret });
  });

  it('basic_text without consent: asks, and on "no" keeps it in memory only (LAI-DSK-008): no file', async () => {
    const { store, consent, safe } = rig('basic_text', { answer: false });
    expect(await store.save(sample())).toEqual({ where: 'memory', reason: 'no-keyring' });
    expect(consent.asked).toBe(1);
    expect(consent.remembered).toBe(0);
    expect(safe.plain).not.toHaveBeenCalled();
    await expect(stat(tokenFilePath(dir))).rejects.toMatchObject({ code: 'ENOENT' });
    expect(await store.load()).toEqual({ kind: 'none' });
  });

  it('basic_text, the user agrees: the consent is remembered, the file is written 0600', async () => {
    const { store, consent, safe } = rig('basic_text', { answer: true });
    const secret = sample();
    expect(await store.save(secret)).toEqual({ where: 'file' });
    expect(consent.asked).toBe(1);
    expect(consent.remembered).toBe(1);
    expect(safe.plain).toHaveBeenCalledWith(true);
    expect(await mode()).toBe(0o600);
    expect(await store.load()).toEqual({ kind: 'ok', secret });
  });

  it('basic_text with the consent already given (settings.secrets.allowPlaintext): no question', async () => {
    const { store, consent } = rig('basic_text', { allowed: true });
    expect(await store.save(sample())).toEqual({ where: 'file' });
    expect(consent.asked).toBe(0);
    expect(consent.remembered).toBe(0);
  });

  it('a later start with the consent given reads the file back (the in-memory key is switched on first)', async () => {
    const first = rig('basic_text', { allowed: true });
    const secret = sample();
    await first.store.save(secret);
    const second = rig('basic_text', { allowed: true });
    expect(await second.store.load()).toEqual({ kind: 'ok', secret });
    expect(second.safe.plain).toHaveBeenCalledWith(true);
  });

  it('overwrites the earlier file and leaves no temporary file behind', async () => {
    const { store } = rig('gnome_libsecret');
    await store.save(sample());
    const later = sample();
    await store.save(later);
    expect(await store.load()).toEqual({ kind: 'ok', secret: later });
    const { readdir } = await import('node:fs/promises');
    expect(await readdir(dir)).toEqual(['cloud-token.bin']);
  });

  it('encryption that throws is memory only, with no file', async () => {
    const { store, safe } = rig('gnome_libsecret');
    safe.encryptString = () => {
      throw new Error('boom');
    };
    expect(await store.save(sample())).toEqual({ where: 'memory', reason: 'unavailable' });
    await expect(stat(tokenFilePath(dir))).rejects.toMatchObject({ code: 'ENOENT' });
  });
});

describe('load', () => {
  it('no file -> none', async () => {
    expect(await rig('gnome_libsecret').store.load()).toEqual({ kind: 'none' });
  });

  it('the keyring changed (decrypt throws) -> unreadable, which the caller turns into "unlinked" and LAI-DSK-011', async () => {
    const { store, safe } = rig('gnome_libsecret');
    await store.save(sample());
    safe.keyLost = true;
    expect(await store.load()).toEqual({ kind: 'unreadable' });
  });

  it.each([
    ['garbage bytes', Buffer.from('not encrypted at all')],
    ['a decrypted text that is not JSON', Buffer.concat([Buffer.from('ENC1'), Buffer.from('plain words').reverse()])],
    ['JSON of another version', Buffer.concat([Buffer.from('ENC1'), Buffer.from('{"v":2,"token":"x"}').reverse()])],
    ['JSON with no token', Buffer.concat([Buffer.from('ENC1'), Buffer.from('{"v":1,"tokenId":"a","expiresAt":"b"}').reverse()])],
  ])('%s -> unreadable', async (_name, bytes) => {
    const { store } = rig('gnome_libsecret');
    await writeFile(tokenFilePath(dir), bytes);
    expect(await store.load()).toEqual({ kind: 'unreadable' });
  });

  it('a path that is a directory -> unreadable (not a crash)', async () => {
    const { mkdir } = await import('node:fs/promises');
    await mkdir(tokenFilePath(dir));
    expect(await rig('gnome_libsecret').store.load()).toEqual({ kind: 'unreadable' });
  });
});

describe('clear', () => {
  it('deletes the file; clearing nothing is fine', async () => {
    const { store } = rig('gnome_libsecret');
    await store.clear();
    await store.save(sample());
    await store.clear();
    await expect(stat(tokenFilePath(dir))).rejects.toMatchObject({ code: 'ENOENT' });
    expect(await store.load()).toEqual({ kind: 'none' });
  });
});
