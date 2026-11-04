import { ArchiveFetchError, archiveUrlFor, GithubArchiveFetcher, isAllowedArchiveUrl } from './archive-fetcher';

const ok = (body: string | Uint8Array, headers: Record<string, string> = {}) => new Response(body as BodyInit, { status: 200, headers });
const redirect = (location: string, status = 302) => new Response(null, { status, headers: { location } });

async function failure(promise: Promise<unknown>): Promise<string> {
  try {
    await promise;
  } catch (error) {
    if (error instanceof ArchiveFetchError) return error.reason;
    throw error;
  }
  return 'accepted';
}

describe('archive URL', () => {
  it('builds the GitHub archive URL for a catalog tag', () => {
    expect(archiveUrlFor('org-alpha/lab-catalog', 'catalog-2026.11.02-1')).toBe(
      'https://github.com/org-alpha/lab-catalog/archive/refs/tags/catalog-2026.11.02-1.tar.gz',
    );
  });

  it.each([
    ['org-alpha/lab-catalog', 'catalog-stable'],
    ['org-alpha/lab-catalog', 'main'],
    ['org-alpha/lab-catalog', 'catalog-2026.11.02-1/../../x'],
    ['evil.example/org/repo', 'catalog-2026.11.02-1'],
    ['org-alpha', 'catalog-2026.11.02-1'],
    ['org/..', 'catalog-2026.11.02-1'],
    ['org/repo?x=1', 'catalog-2026.11.02-1'],
  ])('refuses %s at %s', (repo, tag) => {
    expect(archiveUrlFor(repo, tag)).toBeNull();
  });

  it('allows only https on the two GitHub hosts', () => {
    const withUser = new URL('https://github.com/a');
    withUser.username = 'someone';
    expect(isAllowedArchiveUrl(withUser)).toBe(false);
    expect(isAllowedArchiveUrl(new URL('https://github.com/a/b'))).toBe(true);
    expect(isAllowedArchiveUrl(new URL('https://codeload.github.com/a/b'))).toBe(true);
    for (const url of ['http://github.com/a', 'https://github.com.evil.example/a', 'https://evil.example/github.com', 'https://github.com:8443/a', 'https://127.0.0.1/a', 'file:///etc/passwd']) {
      expect(isAllowedArchiveUrl(new URL(url))).toBe(false);
    }
  });
});

describe('GithubArchiveFetcher', () => {
  const START = 'https://github.com/org-alpha/lab-catalog/archive/refs/tags/catalog-2026.11.02-1.tar.gz';

  it('follows the redirect to codeload and returns the bytes', async () => {
    const calls: string[] = [];
    const fetcher = new GithubArchiveFetcher(async (url, init) => {
      calls.push(url);
      expect(init.redirect).toBe('manual');
      return calls.length === 1 ? redirect('https://codeload.github.com/org-alpha/lab-catalog/tar.gz/refs/tags/catalog-2026.11.02-1') : ok('archive-bytes');
    });
    expect((await fetcher.fetch(START)).toString()).toBe('archive-bytes');
    expect(calls).toHaveLength(2);
  });

  it('refuses a redirect to another host, to http and a redirect loop', async () => {
    expect(await failure(new GithubArchiveFetcher(async () => redirect('https://evil.example/x.tar.gz')).fetch(START))).toBe('redirect_refused');
    expect(await failure(new GithubArchiveFetcher(async () => redirect('http://codeload.github.com/x')).fetch(START))).toBe('redirect_refused');
    expect(await failure(new GithubArchiveFetcher(async () => redirect('https://github.com/again')).fetch(START))).toBe('redirect_refused');
  });

  it('reports 404 as not_found and other statuses as http_error', async () => {
    expect(await failure(new GithubArchiveFetcher(async () => new Response('no', { status: 404 })).fetch(START))).toBe('not_found');
    expect(await failure(new GithubArchiveFetcher(async () => new Response('no', { status: 503 })).fetch(START))).toBe('http_error');
  });

  it('stops at the size limit, declared or streamed', async () => {
    const declared = new GithubArchiveFetcher(async () => ok('x', { 'content-length': '999999' }), 1000);
    expect(await failure(declared.fetch(START))).toBe('too_large');
    const streamed = new GithubArchiveFetcher(async () => ok(new Uint8Array(5000)), 1000);
    expect(await failure(streamed.fetch(START))).toBe('too_large');
    const exact = new GithubArchiveFetcher(async () => ok(new Uint8Array(1000)), 1000);
    expect((await exact.fetch(START)).length).toBe(1000);
  });

  it('times out', async () => {
    const hang = new GithubArchiveFetcher((_url, init) => new Promise((_resolve, reject) => init.signal?.addEventListener('abort', () => reject(new Error('aborted')))), 1000, 50);
    expect(await failure(hang.fetch(START))).toBe('timeout');
  });

  it('maps a network error to network without leaking its text', async () => {
    const fetcher = new GithubArchiveFetcher(async () => {
      throw new Error('getaddrinfo ENOTFOUND secret-host');
    });
    try {
      await fetcher.fetch(START);
      throw new Error('expected a failure');
    } catch (error) {
      expect(error).toBeInstanceOf(ArchiveFetchError);
      expect((error as ArchiveFetchError).reason).toBe('network');
      expect((error as Error).message).not.toContain('secret-host');
    }
  });
});
