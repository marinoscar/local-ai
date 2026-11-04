import { mkdir, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';
import { SHELL_CSP, applyShellSecurityHeaders, shellSecurityHeaders } from '../../src/main/csp';
import {
  PRIVILEGES,
  createLocalaiHandler,
  installLocalaiProtocol,
  mapPath,
  registerLocalaiScheme,
  rendererRoot,
  resolveRendererPath,
  shellUrl,
} from '../../src/main/protocol';

const SECRET = 'top-secret-outside-the-root';
let base: string;
let root: string;
let handler: (request: Request) => Promise<Response>;

beforeAll(async () => {
  base = await mkdtemp(join(tmpdir(), 'ai-desktop-protocol-'));
  root = join(base, 'renderer');
  await mkdir(join(root, 'topbar'), { recursive: true });
  await mkdir(join(root, 'error'), { recursive: true });
  await mkdir(join(root, 'assets'), { recursive: true });
  await writeFile(join(root, 'topbar', 'index.html'), '<!doctype html><title>topbar</title>');
  await writeFile(join(root, 'error', 'index.html'), '<!doctype html><title>error</title>');
  await writeFile(join(root, 'assets', 'app-1.js'), 'export const a = 1;');
  await writeFile(join(root, 'assets', 'app-1.css'), 'body{margin:0}');
  await writeFile(join(root, 'assets', 'font.woff2'), Buffer.from([0, 1, 2, 3]));
  await writeFile(join(root, 'assets', 'icon.svg'), '<svg xmlns="http://www.w3.org/2000/svg"/>');
  await writeFile(join(root, 'assets', '.hidden'), 'x');
  await writeFile(join(base, 'outside.txt'), SECRET);
  await symlink(join(base, 'outside.txt'), join(root, 'assets', 'link.txt'));
  handler = createLocalaiHandler(root);
});
afterAll(async () => {
  await rm(base, { recursive: true, force: true });
});

const get = (url: string, init?: RequestInit) => handler(new Request(url, init));

function expectShellHeaders(response: Response): void {
  expect(response.headers.get('Content-Security-Policy')).toBe(SHELL_CSP);
  expect(response.headers.get('X-Content-Type-Options')).toBe('nosniff');
}

describe('CSP', () => {
  it('is exactly the policy of desktop.md section 3.7', () => {
    expect(SHELL_CSP).toBe(
      "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
        "frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'",
    );
    expect(shellSecurityHeaders()).toEqual({ 'Content-Security-Policy': SHELL_CSP, 'X-Content-Type-Options': 'nosniff' });
  });

  it("allows 'unsafe-inline' for styles only, and no remote source anywhere", () => {
    for (const directive of SHELL_CSP.split('; ')) {
      if (!directive.startsWith('style-src')) expect(directive).not.toContain('unsafe-inline');
      expect(directive).not.toMatch(/https?:|\*|unsafe-eval/);
    }
  });

  it('applyShellSecurityHeaders overwrites a weaker header', () => {
    const headers = applyShellSecurityHeaders(new Headers({ 'Content-Security-Policy': 'default-src *' }));
    expect(headers.get('Content-Security-Policy')).toBe(SHELL_CSP);
  });
});

describe('registration', () => {
  it('registers localai as standard, secure and fetch-capable, nothing more', () => {
    expect(PRIVILEGES).toEqual({ standard: true, secure: true, supportFetchAPI: true });
    const registerSchemesAsPrivileged = vi.fn();
    registerLocalaiScheme({ registerSchemesAsPrivileged });
    expect(registerSchemesAsPrivileged).toHaveBeenCalledWith([{ scheme: 'localai', privileges: { standard: true, secure: true, supportFetchAPI: true } }]);
  });

  it('serves the default session and persist:lab, not persist:agent', async () => {
    const handles: string[] = [];
    const ses = (name: string) => ({ protocol: { handle: (scheme: string) => handles.push(`${name}:${scheme}`) } });
    installLocalaiProtocol([ses('default'), ses('lab')] as never, root);
    expect(handles).toEqual(['default:localai', 'lab:localai']);
  });

  it('knows where the pages are and how to address one', () => {
    expect(rendererRoot('/opt/ai-desktop/resources/app.asar')).toBe('/opt/ai-desktop/resources/app.asar/out/renderer');
    expect(shellUrl('error', '?code=LAI-DSK-004')).toBe('localai://app/error/?code=LAI-DSK-004');
    expect(shellUrl('topbar')).toBe('localai://app/topbar/');
  });
});

describe('serving files', () => {
  it.each([
    ['localai://app/topbar/', 'text/html; charset=utf-8', '<title>topbar</title>'],
    ['localai://app/topbar', 'text/html; charset=utf-8', '<title>topbar</title>'],
    ['localai://app/topbar/index.html', 'text/html; charset=utf-8', '<title>topbar</title>'],
    ['localai://app/error/?code=LAI-DSK-004', 'text/html; charset=utf-8', '<title>error</title>'],
    ['localai://app/assets/app-1.js', 'text/javascript; charset=utf-8', 'export const a = 1;'],
    ['localai://app/assets/app-1.css', 'text/css; charset=utf-8', 'body{margin:0}'],
    ['localai://app/assets/icon.svg', 'image/svg+xml', '<svg'],
  ])('%s', async (url, type, fragment) => {
    const response = await get(url);
    expect(response.status).toBe(200);
    expect(response.headers.get('Content-Type')).toBe(type);
    expect(await response.text()).toContain(fragment);
    expectShellHeaders(response);
  });

  it('serves binary files byte for byte, with a length', async () => {
    const response = await get('localai://app/assets/font.woff2');
    expect(response.headers.get('Content-Type')).toBe('font/woff2');
    expect(response.headers.get('Content-Length')).toBe('4');
    expect([...new Uint8Array(await response.arrayBuffer())]).toEqual([0, 1, 2, 3]);
  });

  it('HEAD answers with the headers and no body', async () => {
    const response = await get('localai://app/topbar/', { method: 'HEAD' });
    expect(response.status).toBe(200);
    expect(await response.text()).toBe('');
    expectShellHeaders(response);
  });

  it('falls back to the page index.html for a path inside a page, with the CSP', async () => {
    for (const url of ['localai://app/error/some/route', 'localai://app/topbar/settings']) {
      const response = await get(url);
      expect(response.status, url).toBe(200);
      expect(await response.text()).toContain('<!doctype html>');
      expectShellHeaders(response);
    }
  });

  it('answers 404 (with the CSP) for what does not exist: no fallback for assets, files with an extension, unknown pages or the root', async () => {
    for (const url of [
      'localai://app/assets/missing.js',
      'localai://app/assets/nope',
      'localai://app/topbar/missing.js',
      'localai://app/unknown-page/',
      'localai://app/',
      'localai://app',
    ]) {
      const response = await get(url);
      expect(response.status, url).toBe(404);
      expectShellHeaders(response);
    }
  });

  it('refuses other hosts, other schemes and other methods, with the CSP', async () => {
    const other = await get('localai://other/topbar/');
    expect(other.status).toBe(404);
    expectShellHeaders(other);
    const post = await get('localai://app/topbar/', { method: 'POST', body: 'x' });
    expect(post.status).toBe(405);
    expect(post.headers.get('Allow')).toBe('GET, HEAD');
    expectShellHeaders(post);
    const http = await get('http://example.com/topbar/');
    expect(http.status).toBe(404);
    expectShellHeaders(http);
  });

  it('answers 500 with the CSP when the file cannot be read', async () => {
    const broken = createLocalaiHandler(join(base, 'does-not-exist'));
    const response = await broken(new Request('localai://app/topbar/'));
    expect([404, 500]).toContain(response.status);
    expectShellHeaders(response);
  });
});

describe('path traversal', () => {
  const attempts = [
    'localai://app/../../etc/passwd',
    'localai://app/%2e%2e/%2e%2e/etc/passwd',
    'localai://app/%2E%2E/%2E%2E/etc/passwd',
    'localai://app/assets/../../outside.txt',
    'localai://app/assets/%2e%2e%2f%2e%2e%2foutside.txt',
    'localai://app/assets/..%2f..%2foutside.txt',
    'localai://app/assets/..%5c..%5coutside.txt',
    'localai://app/assets/%252e%252e/outside.txt',
    'localai://app/assets/%00',
    'localai://app/assets/app-1.js%00.png',
    'localai://app/assets/link.txt',
    'localai://app/assets/.hidden',
    'localai://app/%2e%2e',
  ];
  it.each(attempts)('%s is refused and never returns the secret', async (url) => {
    const response = await get(url);
    expect([400, 403, 404]).toContain(response.status);
    expect(await response.text()).not.toContain(SECRET);
    expectShellHeaders(response);
  });

  it('mapPath refuses every spelling that decodes to a parent segment, NUL or backslash', () => {
    for (const p of ['/../x', '/a/../../x', '/..%2fx', '/%2e%2e/x', '/a/%2e%2e%2f..%2fx', '/a%00b', '/a%5cb', '/a\\b', '/.git/config', '/%E0%A4%A']) {
      expect(mapPath(root, p), p).toBeNull();
    }
    expect(mapPath(root, '/assets/app-1.js')).toEqual({ abs: join(root, 'assets', 'app-1.js'), segments: ['assets', 'app-1.js'] });
    expect(mapPath(root, '/')).toEqual({ abs: root, segments: [] });
  });

  it('resolveRendererPath reports refused, missing and file apart', async () => {
    expect(await resolveRendererPath(root, '/../outside.txt')).toEqual({ kind: 'refused' });
    expect(await resolveRendererPath(root, '/assets/link.txt')).toEqual({ kind: 'refused' });
    expect(await resolveRendererPath(root, '/assets/missing.js')).toEqual({ kind: 'missing' });
    expect(await resolveRendererPath(root, '/topbar/')).toEqual({ kind: 'file', file: join(root, 'topbar', 'index.html') });
  });
});
