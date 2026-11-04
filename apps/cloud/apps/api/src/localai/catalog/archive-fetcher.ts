// Downloading a `catalog-<version>` release archive from GitHub (docs/specs/cloud-app.md section 4.6).
//
// Behind a port so the import job's tests never touch the network. The real implementation is conservative on
// purpose: HTTPS only, the two GitHub hosts only (the archive URL redirects from github.com to codeload.github.com),
// a hard size ceiling checked while streaming, and one overall deadline. The repository name comes from a setting,
// so it is validated before it is put in a URL.
import { ARCHIVE_FETCH_TIMEOUT_MS, MAX_ARCHIVE_BYTES } from './catalog.constants';
import { versionOfCatalogTag } from './version';

/** The port the import job downloads through. */
export const CATALOG_ARCHIVE_FETCHER = Symbol('LOCALAI_CATALOG_ARCHIVE_FETCHER');

export interface CatalogArchiveFetcher {
  /** The bytes of the `.tar.gz` at `url`. Throws `ArchiveFetchError`. */
  fetch(url: string): Promise<Buffer>;
}

export type ArchiveFetchReason = 'not_found' | 'too_large' | 'timeout' | 'http_error' | 'redirect_refused' | 'network';

export class ArchiveFetchError extends Error {
  constructor(
    readonly reason: ArchiveFetchReason,
    message: string,
  ) {
    super(message);
    this.name = 'ArchiveFetchError';
  }
}

const REPO_RE = /^[A-Za-z0-9_.-]{1,100}\/[A-Za-z0-9_.-]{1,100}$/;
const ALLOWED_HOSTS = new Set(['github.com', 'codeload.github.com']);
const MAX_REDIRECTS = 5;

/** `https://github.com/<repo>/archive/refs/tags/<tag>.tar.gz`, or null when the repo or the tag is not acceptable. */
export function archiveUrlFor(repo: string, tag: string): string | null {
  if (!REPO_RE.test(repo) || repo.includes('..') || versionOfCatalogTag(tag) === null) return null;
  return `https://github.com/${repo}/archive/refs/tags/${encodeURIComponent(tag)}.tar.gz`;
}

/** Whether a (redirect) URL may be followed. */
export function isAllowedArchiveUrl(url: URL): boolean {
  return url.protocol === 'https:' && ALLOWED_HOSTS.has(url.hostname) && url.port === '' && url.username === '' && url.password === '';
}

type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export class GithubArchiveFetcher implements CatalogArchiveFetcher {
  constructor(
    private readonly fetchImpl: FetchLike = (input, init) => fetch(input, init),
    private readonly maxBytes: number = MAX_ARCHIVE_BYTES,
    private readonly timeoutMs: number = ARCHIVE_FETCH_TIMEOUT_MS,
  ) {}

  async fetch(url: string): Promise<Buffer> {
    const signal = AbortSignal.timeout(this.timeoutMs);
    try {
      let current = new URL(url);
      for (let hop = 0; hop <= MAX_REDIRECTS; hop += 1) {
        if (!isAllowedArchiveUrl(current)) throw new ArchiveFetchError('redirect_refused', 'The download was redirected somewhere that is not GitHub.');
        const response = await this.fetchImpl(current.toString(), {
          redirect: 'manual',
          signal,
          headers: { 'user-agent': 'local-ai-cloud-catalog-import', accept: 'application/gzip, application/octet-stream' },
        });
        if (response.status >= 300 && response.status < 400) {
          const location = response.headers.get('location');
          await response.body?.cancel().catch(() => undefined);
          if (!location) throw new ArchiveFetchError('http_error', `GitHub answered ${response.status} without a location.`);
          current = new URL(location, current);
          continue;
        }
        if (response.status === 404) throw new ArchiveFetchError('not_found', 'GitHub has no such release tag.');
        if (!response.ok) throw new ArchiveFetchError('http_error', `GitHub answered ${response.status}.`);
        return await this.readLimited(response);
      }
      throw new ArchiveFetchError('redirect_refused', 'The download was redirected too many times.');
    } catch (error) {
      if (error instanceof ArchiveFetchError) throw error;
      if (signal.aborted) throw new ArchiveFetchError('timeout', 'The download took longer than 30 seconds.');
      throw new ArchiveFetchError('network', 'The download failed.');
    }
  }

  private async readLimited(response: Response): Promise<Buffer> {
    const declared = Number(response.headers.get('content-length'));
    if (Number.isFinite(declared) && declared > this.maxBytes) {
      await response.body?.cancel().catch(() => undefined);
      throw new ArchiveFetchError('too_large', 'The release archive is larger than 20 MB.');
    }
    if (!response.body) throw new ArchiveFetchError('http_error', 'GitHub sent no body.');
    const chunks: Buffer[] = [];
    let total = 0;
    const reader = response.body.getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.length;
      if (total > this.maxBytes) {
        await reader.cancel().catch(() => undefined);
        throw new ArchiveFetchError('too_large', 'The release archive is larger than 20 MB.');
      }
      chunks.push(Buffer.from(value));
    }
    return Buffer.concat(chunks, total);
  }
}
