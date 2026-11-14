// The object store the music feature writes to: the platform storage slice's `STORAGE_PROVIDER` (the deployment's
// bucket, configured at runtime on /admin/settings/storage), behind a small port so the service is tested without one.
// Every key is built here from a server-chosen request id and index (`buildObjectKey`), never from a client's string:
// a signed PUT is an unconditional overwrite of exactly its key.
import { Readable } from 'node:stream';
import { Inject, Injectable } from '@nestjs/common';
import { STORAGE_PROVIDER, StorageNotConfiguredError, buildObjectKey, type StorageProvider } from '@marinoscar/platform-api/storage';

import { MUSIC_KEY_PREFIX_ID } from './music-limits';

/** Injection token of the {@link MusicObjectStore}. */
export const MUSIC_OBJECT_STORE = Symbol('MUSIC_OBJECT_STORE');

export interface MusicObjectStore {
  /** Stores `bytes` under `key` (the sample). */
  put(key: string, bytes: Buffer, mime: string): Promise<void>;
  /** A signed GET of `key` for `ttlS` seconds; `filename` sets the download name (`attachment`) or none (`inline`). */
  signedGet(key: string, ttlS: number, disposition?: { filename: string; attachment: boolean }): Promise<string>;
  /** A signed single-request PUT of `key` for `ttlS` seconds, with no content type bound (the node sends its own). */
  signedPut(key: string, ttlS: number): Promise<string>;
  /** Deletes `key`; deleting a missing object is not an error. */
  delete(key: string): Promise<void>;
}

/** Thrown by the store when the deployment has no object storage configured (the routes answer 503). */
export class MusicStorageNotConfigured extends Error {
  constructor() {
    super('object storage is not configured');
    this.name = 'MusicStorageNotConfigured';
  }
}

/** `localai-music/samples/<request id>.<ext>` (cloud-app.md section 3.9). */
export const sampleKey = (requestId: string, ext: string): string => buildObjectKey(MUSIC_KEY_PREFIX_ID, {}, 'samples', `${requestId}.${ext}`);
/** `localai-music/songs/<request id>/<index>` (no extension: the node chooses the format; the download names it). */
export const songKey = (requestId: string, index: number): string => buildObjectKey(MUSIC_KEY_PREFIX_ID, {}, 'songs', requestId, String(index));

/** A `Content-Disposition` value with a plain ASCII file name (quotes and non-ASCII replaced). */
export function contentDisposition(filename: string, attachment: boolean): string {
  const safe = filename.replace(/[^A-Za-z0-9 ._()-]/g, '_').slice(0, 100) || 'song';
  return `${attachment ? 'attachment' : 'inline'}; filename="${safe}"`;
}

@Injectable()
export class StorageMusicObjectStore implements MusicObjectStore {
  constructor(@Inject(STORAGE_PROVIDER) private readonly storage: StorageProvider) {}

  async put(key: string, bytes: Buffer, mime: string): Promise<void> {
    await this.guard(() => this.storage.upload(key, Readable.from(bytes), { mimeType: mime, contentLength: bytes.length }));
  }

  signedGet(key: string, ttlS: number, disposition?: { filename: string; attachment: boolean }): Promise<string> {
    return this.guard(() =>
      this.storage.getSignedDownloadUrl(key, {
        expiresIn: ttlS,
        ...(disposition ? { responseContentDisposition: contentDisposition(disposition.filename, disposition.attachment) } : {}),
      }),
    );
  }

  signedPut(key: string, ttlS: number): Promise<string> {
    return this.guard(() => this.storage.getSignedPutUrl(key, { expiresIn: ttlS }));
  }

  async delete(key: string): Promise<void> {
    await this.guard(() => this.storage.delete(key));
  }

  private async guard<T>(call: () => Promise<T>): Promise<T> {
    try {
      return await call();
    } catch (error) {
      if (error instanceof StorageNotConfiguredError) throw new MusicStorageNotConfigured();
      throw error;
    }
  }
}
