// =============================================================================
// The AI slice's object store, over the app's storage
// =============================================================================
//
// `@marinoscar/platform-api/ai` reaches object storage only through its
// `AI_OBJECT_STORE` port: the four provider calls its output writer and input
// resolver make, plus the configuration questions they ask. This adapter
// answers them with the storage slice's `STORAGE_PROVIDER` and
// `StorageConfigService` (`@marinoscar/platform-api/storage`), exactly as the AI code called them before the move.
// =============================================================================

import type { Readable } from 'node:stream';

import { Inject, Injectable } from '@nestjs/common';
import type { AiObjectStore } from '@marinoscar/platform-api/ai';

import {
  STORAGE_PROVIDER,
  STORAGE_SETTINGS_PATH,
  StorageConfigService,
  StorageNotConfiguredError,
  type StorageProvider,
} from '@marinoscar/platform-api/storage';

@Injectable()
export class AiObjectStoreAdapter implements AiObjectStore {
  readonly settingsPath = STORAGE_SETTINGS_PATH;

  constructor(
    @Inject(STORAGE_PROVIDER) private readonly storage: StorageProvider,
    private readonly storageConfig: StorageConfigService,
  ) {}

  async upload(key: string, body: Readable, opts: { mimeType: string; contentLength: number }): Promise<{ bucket: string }> {
    const result = await this.storage.upload(key, body, opts);
    return { bucket: result.bucket };
  }

  delete(key: string): Promise<void> {
    return this.storage.delete(key);
  }

  download(key: string): Promise<Readable> {
    return this.storage.download(key);
  }

  getSignedDownloadUrl(key: string, opts: { expiresIn: number }): Promise<string> {
    return this.storage.getSignedDownloadUrl(key, opts);
  }

  async assertWritable(): Promise<void> {
    const resolution = await this.storageConfig.resolve();
    if (!resolution.configured) {
      throw StorageNotConfiguredError.missing(resolution.provider, resolution.missing);
    }
  }

  activeProvider(): Promise<string> {
    return this.storageConfig.activeProvider();
  }

  notConfiguredReason(err: unknown): string | null {
    if (!(err instanceof StorageNotConfiguredError)) return null;
    const body = err.getResponse() as { details?: { reason?: string } };
    return body.details?.reason ?? 'storage_not_configured';
  }
}
