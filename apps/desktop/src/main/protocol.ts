// The localai://app protocol: serves the shell pages (out/renderer) out of app.asar (docs/specs/desktop.md 3.7).
//
// Why a protocol and not file://: the `grantFileProtocolExtraPrivileges` fuse is off (section 3.3), so Electron no
// longer serves file:// URLs from inside app.asar. The handler reads the files with `fs`, which Electron's asar
// support makes work in the main process, and answers every request (file, index fallback, refusal) with the CSP.
import { readFile, realpath, stat } from 'node:fs/promises';
import { extname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import type { Protocol, Session } from 'electron';
import { applyShellSecurityHeaders } from './csp';

export const MODULE = 'protocol' as const;

export const SCHEME = 'localai';
export const HOST = 'app';
export const PRIVILEGES = { standard: true, secure: true, supportFetchAPI: true } as const;

const MIME: Record<string, string> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.txt': 'text/plain; charset=utf-8',
};

/** Must run before `app.whenReady()`. */
export function registerLocalaiScheme(protocol: Pick<Protocol, 'registerSchemesAsPrivileged'>): void {
  protocol.registerSchemesAsPrivileged([{ scheme: SCHEME, privileges: { ...PRIVILEGES } }]);
}

export type Resolution =
  | { kind: 'file'; file: string }
  | { kind: 'refused' }
  | { kind: 'missing' };

/**
 * Maps a request pathname to a path under `root`, or refuses it. The pathname is decoded once and must then hold no
 * `..` segment, no NUL and no backslash; the joined path must still be inside `root` (`path.relative()` check).
 * It does not touch the disk.
 */
export function mapPath(root: string, pathname: string): { abs: string; segments: string[] } | null {
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return null;
  }
  if (decoded.includes('\0') || decoded.includes('\\')) return null;
  const segments = decoded.split('/').filter((s) => s !== '');
  if (segments.some((s) => s === '..' || s === '.')) return null;
  // Dotfiles are never part of a built page.
  if (segments.some((s) => s.startsWith('.'))) return null;
  const abs = resolve(root, ...segments);
  const rel = relative(root, abs);
  if (rel === '..' || rel.startsWith(`..${sep}`) || isAbsolute(rel)) return null;
  return { abs, segments };
}

async function isFile(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isFile();
  } catch {
    return false;
  }
}

async function isDirectory(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isDirectory();
  } catch {
    return false;
  }
}

/** Resolves a pathname to a file: itself, a folder's index.html, or the page's index.html (never for assets). */
export async function resolveRendererPath(root: string, pathname: string): Promise<Resolution> {
  const mapped = mapPath(root, pathname);
  if (!mapped) return { kind: 'refused' };
  const { abs, segments } = mapped;
  let file: string | null = null;
  if (await isFile(abs)) file = abs;
  else if (await isDirectory(abs)) {
    const index = join(abs, 'index.html');
    if (await isFile(index)) file = index;
  } else {
    // SPA-style fallback: /error/anything -> /error/index.html. A missing script, style or font stays a 404.
    const page = segments[0];
    const last = segments[segments.length - 1] ?? '';
    if (page !== undefined && page !== 'assets' && !last.includes('.')) {
      const index = join(root, page, 'index.html');
      if (await isFile(index)) file = index;
    }
  }
  if (file === null) return { kind: 'missing' };
  // A symlink inside the folder must not lead out of it. (Not checked inside an asar, which has no such links.)
  if (!root.includes('.asar')) {
    try {
      const real = await realpath(file);
      const rel = relative(await realpath(root), real);
      if (rel === '..' || rel.startsWith(`..${sep}`) || isAbsolute(rel)) return { kind: 'refused' };
    } catch {
      return { kind: 'missing' };
    }
  }
  return { kind: 'file', file };
}

function respond(status: number, headers: Record<string, string>, body: Uint8Array | string | null): Response {
  const h = applyShellSecurityHeaders(new Headers(headers));
  return new Response(body, { status, headers: h });
}

/** The request handler for `protocol.handle('localai', ...)`. Every response carries the CSP and nosniff. */
export function createLocalaiHandler(root: string): (request: Request) => Promise<Response> {
  const base = resolve(root);
  return async (request) => {
    try {
      if (request.method !== 'GET' && request.method !== 'HEAD') {
        return respond(405, { Allow: 'GET, HEAD', 'Content-Type': 'text/plain; charset=utf-8' }, 'Method not allowed');
      }
      let url: URL;
      try {
        url = new URL(request.url);
      } catch {
        return respond(400, { 'Content-Type': 'text/plain; charset=utf-8' }, 'Bad request');
      }
      if (url.protocol !== `${SCHEME}:` || url.hostname !== HOST) {
        return respond(404, { 'Content-Type': 'text/plain; charset=utf-8' }, 'Not found');
      }
      const resolved = await resolveRendererPath(base, url.pathname);
      if (resolved.kind === 'refused') return respond(403, { 'Content-Type': 'text/plain; charset=utf-8' }, 'Forbidden');
      if (resolved.kind === 'missing') return respond(404, { 'Content-Type': 'text/plain; charset=utf-8' }, 'Not found');
      const body = await readFile(resolved.file);
      return respond(
        200,
        {
          'Content-Type': MIME[extname(resolved.file).toLowerCase()] ?? 'application/octet-stream',
          'Content-Length': String(body.byteLength),
          'Cache-Control': 'no-cache',
        },
        request.method === 'HEAD' ? null : new Uint8Array(body),
      );
    } catch {
      return respond(500, { 'Content-Type': 'text/plain; charset=utf-8' }, 'Internal error');
    }
  };
}

/** Where the built shell pages are: `<app.asar>/out/renderer` packaged, `<project>/out/renderer` in development. */
export function rendererRoot(appPath: string): string {
  return join(appPath, 'out', 'renderer');
}

/**
 * Serves `localai://app/*` on the given sessions' protocol registries (the default session for the top bar, error,
 * PIN, ... and `persist:lab` for the Lab view). Not on `persist:agent`: agent pages have no business with shell pages.
 */
export function installLocalaiProtocol(sessions: ReadonlyArray<Pick<Session, 'protocol'>>, root: string): void {
  const handler = createLocalaiHandler(root);
  for (const ses of sessions) ses.protocol.handle(SCHEME, (request) => handler(request));
}

/** `localai://app/<page>/` for a built shell page. */
export function shellUrl(page: string, query = ''): string {
  return `${SCHEME}://${HOST}/${page}/${query}`;
}
