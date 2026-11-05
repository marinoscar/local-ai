// The fake cloud (ai.marin.cr) for the desktop tests (docs/specs/desktop.md section 9). Test-only; never packaged.
//
// It speaks the part of the EnterpriseAppBase API that connected mode uses (EAB docs/DEVICE-AUTH.md, docs/personal-access-tokens.md):
//   POST   /api/auth/device/code    -> { data: { deviceCode, userCode, verificationUri, verificationUriComplete, expiresIn, interval } }
//   POST   /api/auth/device/token   -> 400 { error: authorization_pending | slow_down | expired_token | access_denied }
//                                      or 200 { data: { accessToken: "pat_...", credentialType: "pat", expiresAt, tokenId, ... } }
//   GET    /api/auth/me             -> { data: { id, email, displayName, ... } }   (bearer required)
//   DELETE /api/pat/:id             -> 204                                        (bearer required)
//   GET    /api/localai/nodes       -> held open while `holdNodes` is set (to prove an abort), else { data: [] }
// The app's code never talks to a real host in a test: with LOCALAI_DESKTOP_TEST_CLOUD_PORT the main process connects
// `ai.marin.cr:443` to this server on a loopback port, in plain HTTP (cloud-link.ts `createLoopbackDispatcher`; honoured only
// by an unpackaged run and the e2e test copy). The unit tests of the device flow use their own scripted fakes.
import { randomBytes, randomUUID } from 'node:crypto';
import { type IncomingMessage, type ServerResponse, createServer } from 'node:http';
import type { AddressInfo } from 'node:net';

export const FAKE_CLOUD_HOST = 'ai.marin.cr' as const;

export interface FakeCloudRequest {
  method: string;
  path: string;
  /** The bearer the caller sent, or undefined. */
  bearer: string | undefined;
  body: unknown;
  at: number;
}

export interface FakeCloudOptions {
  /** TCP port on 127.0.0.1; 0 picks a free one. */
  port?: number;
  /** The polling `interval` the grant carries, in seconds (the app clamps it to 1..60). Default 1 so a test does not wait 5 s. */
  interval?: number;
  /** `expiresIn` of the grant, seconds. */
  expiresIn?: number;
  /** The display name `GET /api/auth/me` answers. */
  displayName?: string | null;
}

export interface FakeCloud {
  readonly port: number;
  readonly requests: FakeCloudRequest[];
  /** The `clientInfo` of the last `POST /api/auth/device/code`. */
  readonly clientInfos: unknown[];
  /** Codes handed out so far. */
  readonly userCodes: string[];
  /** TCP connections accepted since the start (a refused request opens none). */
  connections(): number;
  /** `Date.now()` of each accepted TCP connection. */
  readonly connectionTimes: number[];
  /** Requests being held open right now (`holdNodes`, `holdMe`). */
  openRequests(): number;
  /** Times at which a held request was closed by the client (an abort), `Date.now()`. */
  readonly abortedAt: number[];
  /** The PAT issued by the last approved code, or null. */
  readonly issued: { token: string; tokenId: string } | null;
  /** Token ids revoked through `DELETE /api/pat/:id`. */
  readonly revoked: string[];
  /** The user approves the current code. The next poll collects the PAT. */
  approve(): void;
  /** The user denies the current code. */
  deny(): void;
  /** The code runs out. */
  expire(): void;
  /** The next poll answers `slow_down` once. */
  slowDownNext(): void;
  /** Hold `GET /api/localai/nodes` open until the client goes away. */
  holdNodes: boolean;
  /** Hold `GET /api/auth/me` open until the client goes away. */
  holdMe: boolean;
  /** Answer 401 to every request that carries a bearer (a revoked or expired PAT). */
  rejectBearer: boolean;
  displayName: string | null;
  close(): Promise<void>;
}

type CodeState = 'pending' | 'approved' | 'denied' | 'expired' | 'collected';

const envelope = (data: unknown): string => JSON.stringify({ data, meta: { timestamp: new Date().toISOString() } });

async function readJson(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  const text = Buffer.concat(chunks).toString('utf8');
  if (text === '') return null;
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

export async function startFakeCloud(options: FakeCloudOptions = {}): Promise<FakeCloud> {
  const requests: FakeCloudRequest[] = [];
  const clientInfos: unknown[] = [];
  const userCodes: string[] = [];
  const abortedAt: number[] = [];
  const revoked: string[] = [];
  const open = new Set<ServerResponse>();
  let connections = 0;
  const connectionTimes: number[] = [];
  let issued: FakeCloud['issued'] = null;
  let current: { deviceCode: string; userCode: string; state: CodeState } | null = null;
  let slowDown = false;
  let codeSeq = 0;
  const flags = { holdNodes: false, holdMe: false, rejectBearer: false, displayName: options.displayName === undefined ? 'Person A' : options.displayName };

  const send = (res: ServerResponse, status: number, body?: string): void => {
    res.writeHead(status, { 'content-type': 'application/json', connection: 'close' });
    res.end(body);
  };

  const server = createServer((req, res) => {
    void (async () => {
      const url = new URL(req.url ?? '/', 'http://fake-cloud.invalid');
      const method = req.method ?? 'GET';
      const auth = req.headers['authorization'];
      const bearer = typeof auth === 'string' && auth.startsWith('Bearer ') ? auth.slice(7) : undefined;
      const body = method === 'GET' || method === 'DELETE' ? null : await readJson(req);
      requests.push({ method, path: url.pathname, bearer, body, at: Date.now() });

      if (bearer !== undefined && (flags.rejectBearer || bearer !== issued?.token)) return send(res, 401, JSON.stringify({ statusCode: 401, message: 'Unauthorized' }));

      if (method === 'POST' && url.pathname === '/api/auth/device/code') {
        clientInfos.push((body as { clientInfo?: unknown } | null)?.clientInfo ?? null);
        codeSeq += 1;
        current = { deviceCode: randomBytes(24).toString('hex'), userCode: `ABCD-${String(1000 + codeSeq)}`, state: 'pending' };
        userCodes.push(current.userCode);
        return send(
          res,
          201,
          envelope({
            deviceCode: current.deviceCode,
            userCode: current.userCode,
            verificationUri: `https://${FAKE_CLOUD_HOST}/activate`,
            verificationUriComplete: `https://${FAKE_CLOUD_HOST}/activate?code=${current.userCode}`,
            expiresIn: options.expiresIn ?? 900,
            interval: options.interval ?? 1,
          }),
        );
      }
      if (method === 'POST' && url.pathname === '/api/auth/device/token') {
        const deviceCode = (body as { deviceCode?: unknown } | null)?.deviceCode;
        if (!current || deviceCode !== current.deviceCode) return send(res, 400, JSON.stringify({ error: 'expired_token', error_description: 'unknown code' }));
        if (slowDown) {
          slowDown = false;
          return send(res, 400, JSON.stringify({ error: 'slow_down', error_description: 'too fast' }));
        }
        switch (current.state) {
          case 'pending':
            return send(res, 400, JSON.stringify({ error: 'authorization_pending', error_description: 'User has not yet authorized this device' }));
          case 'denied':
            return send(res, 400, JSON.stringify({ error: 'access_denied', error_description: 'denied' }));
          case 'expired':
          case 'collected':
            return send(res, 400, JSON.stringify({ error: 'expired_token', error_description: 'expired' }));
          case 'approved': {
            current.state = 'collected';
            issued = { token: `pat_${randomBytes(24).toString('base64url')}`, tokenId: randomUUID() };
            return send(
              res,
              200,
              envelope({
                accessToken: issued.token,
                tokenType: 'Bearer',
                expiresIn: 7775999,
                credentialType: 'pat',
                expiresAt: new Date(Date.now() + 90 * 86400_000).toISOString(),
                tokenId: issued.tokenId,
                tokenName: 'Device: ai-desktop',
              }),
            );
          }
        }
      }
      if (method === 'GET' && url.pathname === '/api/auth/me') {
        if (bearer === undefined) return send(res, 401, '{}');
        if (flags.holdMe) return hold(res);
        return send(res, 200, envelope({ id: randomUUID(), email: 'person-a@example.test', displayName: flags.displayName, profileImageUrl: null, roles: [], permissions: [] }));
      }
      if (method === 'DELETE' && url.pathname.startsWith('/api/pat/')) {
        if (bearer === undefined) return send(res, 401, '{}');
        revoked.push(decodeURIComponent(url.pathname.slice('/api/pat/'.length)));
        return send(res, 204);
      }
      if (method === 'GET' && url.pathname === '/api/localai/nodes') {
        if (bearer === undefined) return send(res, 401, '{}');
        if (flags.holdNodes) return hold(res);
        return send(res, 200, envelope([]));
      }
      return send(res, 404, JSON.stringify({ statusCode: 404, message: 'Not Found' }));
    })().catch(() => send(res, 500));
  });

  /** Keeps a response open until the client closes the connection; that close is the abort the tests time. */
  function hold(res: ServerResponse): void {
    open.add(res);
    res.on('close', () => {
      if (open.delete(res) && !res.writableEnded) abortedAt.push(Date.now());
    });
  }

  server.on('connection', () => void ((connections += 1), connectionTimes.push(Date.now())));
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(options.port ?? 0, '127.0.0.1', resolve);
  });
  const port = (server.address() as AddressInfo).port;

  return {
    port,
    requests,
    clientInfos,
    userCodes,
    connections: () => connections,
    connectionTimes,
    openRequests: () => open.size,
    abortedAt,
    get issued() {
      return issued;
    },
    revoked,
    approve: () => void (current && (current.state = 'approved')),
    deny: () => void (current && (current.state = 'denied')),
    expire: () => void (current && (current.state = 'expired')),
    slowDownNext: () => void (slowDown = true),
    get holdNodes() {
      return flags.holdNodes;
    },
    set holdNodes(value: boolean) {
      flags.holdNodes = value;
    },
    get holdMe() {
      return flags.holdMe;
    },
    set holdMe(value: boolean) {
      flags.holdMe = value;
    },
    get rejectBearer() {
      return flags.rejectBearer;
    },
    set rejectBearer(value: boolean) {
      flags.rejectBearer = value;
    },
    get displayName() {
      return flags.displayName;
    },
    set displayName(value: string | null) {
      flags.displayName = value;
    },
    close: () =>
      new Promise<void>((resolve, reject) => {
        for (const res of open) res.destroy();
        server.close((err) => (err ? reject(err) : resolve()));
        server.closeAllConnections();
      }),
  };
}
