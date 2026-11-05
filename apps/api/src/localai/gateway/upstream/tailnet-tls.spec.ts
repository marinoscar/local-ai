// Tailnet Pattern B against a REAL local TLS server (docs/specs/gateway.md section 3.5): the gateway dials the node's
// address, presents `<machine>.<suffix>` as SNI and as `Host`, and verifies the certificate against the trusted CA. The CA
// and the server certificate are generated here with the `openssl` CLI into a temporary directory and removed afterwards;
// the CA is injected into the client for the test only (production uses the system bundle). "Wrong name" is a server whose
// certificate is issued for another name: the handshake fails, the failure is a TLS failure, the attempt loop retries it
// and a request that fails everywhere ends as LAI-GW-040.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:https';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { TLSSocket } from 'node:tls';

import { AttemptLoop, type RoutingPort } from '../attempts';
import { DEFAULT_MODEL_POLICY } from '../model-resolver';
import { candidate } from '../routing/routing.fixtures';
import type { SelectedCandidate } from '../routing/routing.types';
import { UpstreamClient, type UpstreamCall } from './client';
import { UpstreamTransportError } from './errors';
import { TailnetTargets } from './targets';

const SUFFIX = 'example-tailnet.ts.net';
const MACHINE = 'node-x';
const GOOD_NAME = `${MACHINE}.${SUFFIX}`;
const LOOPBACK = '127.0.0.1';

interface Pki {
  dir: string;
  caCert: string;
  issue(name: string): { cert: string; key: string };
}

function openssl(args: string[], cwd: string): void {
  execFileSync('openssl', args, { cwd, stdio: 'pipe' });
}

function makePki(): Pki {
  const dir = mkdtempSync(join(tmpdir(), 'localai-tailnet-tls-'));
  openssl(['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', 'ca.key', '-out', 'ca.crt', '-subj', '/CN=Test CA', '-days', '2', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign'], dir);
  let counter = 0;
  return {
    dir,
    caCert: readFileSync(join(dir, 'ca.crt'), 'utf8'),
    issue(name) {
      counter += 1;
      const stem = `srv${counter}`;
      writeFileSync(join(dir, `${stem}.ext`), `subjectAltName=DNS:${name}\nbasicConstraints=CA:FALSE\n`);
      openssl(['req', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', `${stem}.key`, '-out', `${stem}.csr`, '-subj', `/CN=${name}`], dir);
      openssl(['x509', '-req', '-in', `${stem}.csr`, '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-out', `${stem}.crt`, '-days', '2', '-extfile', `${stem}.ext`], dir);
      return { cert: readFileSync(join(dir, `${stem}.crt`), 'utf8'), key: readFileSync(join(dir, `${stem}.key`), 'utf8') };
    },
  };
}

interface Seen {
  host: string | undefined;
  servername: string | false | null | undefined;
  authorization: string | undefined;
  url: string | undefined;
}

async function tlsServer(credentials: { cert: string; key: string }, seen: Seen[]): Promise<{ server: Server; port: number }> {
  const server = createServer(credentials, (request, response) => {
    seen.push({
      host: request.headers.host,
      servername: (request.socket as TLSSocket).servername,
      authorization: request.headers.authorization,
      url: request.url,
    });
    request.resume();
    request.on('end', () => {
      response.writeHead(200, { 'content-type': 'application/json' });
      response.end('{"ok":true}');
    });
  });
  await new Promise<void>((resolve) => server.listen(0, LOOPBACK, resolve));
  return { server, port: (server.address() as AddressInfo).port };
}

const callTo = (overrides: Partial<UpstreamCall> = {}): UpstreamCall => ({
  nodeId: 'ai-n1',
  publicHost: 'ai-n1.example.test',
  route: { servePattern: 'B', tailnetIp: LOOPBACK, tailnetMachine: MACHINE },
  path: '/v1/chat/completions',
  body: Buffer.from('{}'),
  contentType: 'application/json',
  accept: 'application/json',
  requestId: 'req-1',
  apiKey: 'node-credential',
  signal: new AbortController().signal,
  connectMs: 2000,
  firstByteMs: 5000,
  idleMs: 5000,
  ...overrides,
});

describe('tailnet Pattern B over TLS', () => {
  let pki: Pki;
  const servers: Server[] = [];
  const clients: UpstreamClient[] = [];

  beforeAll(() => {
    pki = makePki();
  });
  afterAll(() => {
    rmSync(pki.dir, { recursive: true, force: true });
  });
  afterEach(async () => {
    await Promise.all(clients.splice(0).map((client) => client.close()));
    for (const server of servers.splice(0)) {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  const clientFor = (port: number, options: { trustCa?: boolean; suffix?: string | null; warnings?: string[] } = {}): UpstreamClient => {
    const client = new UpstreamClient({
      version: 'test',
      targets: new TailnetTargets({
        suffix: { read: async () => (options.suffix === undefined ? SUFFIX : options.suffix) as string | null },
        ports: { a: port, b: port },
        warn: (line) => options.warnings?.push(line),
      }),
      ...(options.trustCa === false ? {} : { tlsCa: pki.caCert }),
    });
    clients.push(client);
    return client;
  };

  const start = async (name: string): Promise<{ port: number; seen: Seen[] }> => {
    const seen: Seen[] = [];
    const { server, port } = await tlsServer(pki.issue(name), seen);
    servers.push(server);
    return { port, seen };
  };

  it('dials the address, presents <machine>.<suffix> as SNI and Host, and the verified answer comes back', async () => {
    const { port, seen } = await start(GOOD_NAME);
    const response = await clientFor(port).call(callTo());
    expect(response.status).toBe(200);
    response.destroy();
    expect(seen).toEqual([{ host: GOOD_NAME, servername: GOOD_NAME, authorization: 'Bearer node-credential', url: '/v1/chat/completions' }]);
  });

  it('a certificate issued for another name fails the handshake as a TLS failure', async () => {
    const { port, seen } = await start(`other.${SUFFIX}`);
    await expect(clientFor(port).call(callTo())).rejects.toMatchObject({ name: 'UpstreamTransportError', reason: 'tls' });
    expect(seen).toEqual([]); // the request never reached the server
  });

  it('a certificate from a CA that is not trusted fails as a TLS failure (verification is on)', async () => {
    const { port } = await start(GOOD_NAME);
    await expect(clientFor(port, { trustCa: false }).call(callTo())).rejects.toMatchObject({ reason: 'tls' });
  });

  it('a different machine name is a different TLS name: the pooled connection is not reused across names', async () => {
    const { port, seen } = await start(GOOD_NAME);
    const client = clientFor(port);
    (await client.call(callTo())).destroy();
    await expect(client.call(callTo({ route: { servePattern: 'B', tailnetIp: LOOPBACK, tailnetMachine: 'node-y' } }))).rejects.toBeInstanceOf(UpstreamTransportError);
    expect(seen).toHaveLength(1);
  });

  it('a Pattern B node with no suffix setting is not dialled', async () => {
    const { port, seen } = await start(GOOD_NAME);
    const client = clientFor(port, { suffix: null });
    await expect(client.call(callTo())).rejects.toMatchObject({ reason: 'connect', code: 'NO_TARGET' });
    expect(seen).toEqual([]);
  });

  describe('through the attempt loop', () => {
    const routing = (): RoutingPort => ({
      select: (_model, _policy, tried) => {
        const open: SelectedCandidate[] = [candidate('ai-n1', { servePattern: 'B', tailnetIp: LOOPBACK, tailnetMachine: MACHINE })].filter((c) => !tried.has(c.nodeId));
        return open.length ? { kind: 'candidates', candidates: open } : { kind: 'none_loaded' };
      },
      unavailable: () => ({ status: 503, code: 'LAI-GW-021' }),
      acquire: () => undefined,
      release: () => undefined,
      markSuspect: () => undefined,
      markKeyInvalid: () => undefined,
      markUnloaded: () => undefined,
      recordTtfb: () => undefined,
    });
    const run = async (client: UpstreamClient) => {
      const warnings: string[] = [];
      const loop = new AttemptLoop({ routing: routing(), keys: { get: async () => 'node-credential' }, transport: client, log: { warn: (line) => warnings.push(line) } });
      const outcome = await loop.run({
        requestId: 'req-1',
        model: { modelId: 'model-a', requestedModel: 'model-a', policy: DEFAULT_MODEL_POLICY, roles: null, inApprovedCatalog: true },
        path: '/v1/chat/completions',
        body: Buffer.from('{}'),
        accept: 'application/json',
        maxAttempts: 2,
        timeouts: { connectMs: 2000, firstByteMs: 5000, idleMs: 5000 },
        signal: new AbortController().signal,
      });
      return { outcome, warnings };
    };

    it('wrong certificate name: the failure is retryable and, with no other node, ends as LAI-GW-040', async () => {
      const { port } = await start(`other.${SUFFIX}`);
      const { outcome, warnings } = await run(clientFor(port));
      expect(outcome).toMatchObject({ ok: false, reason: 'error', attempts: 1, error: { localaiCode: 'LAI-GW-040', status: 502 } });
      expect(warnings.map((line) => JSON.parse(line))).toEqual([{ event: 'gateway.attempt_failed', request_id: 'req-1', node_id: 'ai-n1', attempt: 1, reason: 'tls', upstream_status: null }]);
      expect(warnings.join('')).not.toContain(LOOPBACK);
      expect(warnings.join('')).not.toContain(SUFFIX);
    });

    it('right name: commits on the first attempt', async () => {
      const { port } = await start(GOOD_NAME);
      const { outcome } = await run(clientFor(port));
      expect(outcome).toMatchObject({ ok: true, kind: 'success', attempts: 1 });
      if (outcome.ok && outcome.kind === 'success') {
        outcome.response.destroy();
        outcome.release();
      }
    });
  });
});
