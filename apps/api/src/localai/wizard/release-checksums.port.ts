// Where the wizard learns the checksum of the install script: the release's `SHA256SUMS` on GitHub, fetched by the
// server (docs/specs/cloud-app.md section 4.8). A port so tests never reach GitHub; the real one caches.
import { Injectable, Logger } from '@nestjs/common';

import { releaseTag, sha256FromSums, SCRIPT_NAME } from './install-info';

export interface ReleaseChecksums {
  /** The sha256 of the install script of agent release `version`, or null when it cannot be read. Never throws. */
  scriptSha256(repo: string, version: string): Promise<string | null>;
}

/** Nest injection token of the port. */
export const RELEASE_CHECKSUMS = Symbol('RELEASE_CHECKSUMS');

const FETCH_TIMEOUT_MS = 10_000;
const MAX_BYTES = 64 * 1024;
/** A found checksum is kept this long (release assets do not change); a miss only briefly, so a fresh release shows up. */
const HIT_TTL_MS = 60 * 60 * 1000;
const MISS_TTL_MS = 60 * 1000;

@Injectable()
export class GithubReleaseChecksums implements ReleaseChecksums {
  private readonly logger = new Logger(GithubReleaseChecksums.name);
  private readonly cache = new Map<string, { value: string | null; expiresAt: number }>();

  async scriptSha256(repo: string, version: string, now: number = Date.now()): Promise<string | null> {
    const key = `${repo}@${version}`;
    const cached = this.cache.get(key);
    if (cached && cached.expiresAt > now) return cached.value;
    let value: string | null = null;
    try {
      const response = await fetch(`https://github.com/${repo}/releases/download/${releaseTag(version)}/SHA256SUMS`, {
        redirect: 'follow',
        signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
      });
      if (response.ok) value = sha256FromSums((await response.text()).slice(0, MAX_BYTES), SCRIPT_NAME);
    } catch (error) {
      // The error NAME only: the cause is the network's, not ours to log.
      this.logger.warn(`could not read SHA256SUMS of ${releaseTag(version)}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
    this.cache.set(key, { value, expiresAt: now + (value === null ? MISS_TTL_MS : HIT_TTL_MS) });
    return value;
  }
}
