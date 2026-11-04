// A skeleton fake of the node agent for the desktop tests (docs/specs/desktop.md section 9): an HTTP server on
// 127.0.0.1:7070 that answers GET /api/v1/health like the real agent (agent/api/openapi.yaml, getHealth).
// Later items add the session, status, events and mode routes the specs need. Test-only; never packaged.
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import type { IncomingMessage, Server } from 'node:http';

export interface FakeAgentOptions {
  /** Default 7070, the agent's port. Pass 0 to let the OS pick a free one (unit tests). */
  port?: number;
  /** Always loopback: the real agent listens on loopback only. */
  host?: '127.0.0.1';
  mode?: string;
  version?: string;
  starting?: boolean;
}

export interface FakeAgent {
  readonly url: string;
  readonly port: number;
  /** Every request the fake saw, in order (method and path only). */
  readonly requests: Array<{ method: string; path: string }>;
  close(): Promise<void>;
}

export async function startFakeAgent(options: FakeAgentOptions = {}): Promise<FakeAgent> {
  const host = options.host ?? '127.0.0.1';
  const requests: FakeAgent['requests'] = [];
  const server: Server = createServer((req: IncomingMessage, res) => {
    const path = (req.url ?? '/').split('?')[0] ?? '/';
    requests.push({ method: req.method ?? 'GET', path });
    if (req.method === 'GET' && path === '/api/v1/health') {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          version: options.version ?? '0.1.0',
          mode: options.mode ?? 'online',
          starting: options.starting ?? false,
        }),
      );
      return;
    }
    res.writeHead(404, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ error: 'not_found' }));
  });
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(options.port ?? 7070, host, resolve);
  });
  const port = (server.address() as AddressInfo).port;
  return {
    url: `http://${host}:${port}`,
    port,
    requests,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()));
        server.closeAllConnections();
      }),
  };
}
