// The fake cloud (ai.marin.cr) for the desktop tests (docs/specs/desktop.md section 9). Test-only; never packaged.
//
// It speaks the part of the EnterpriseAppBase API that connected mode uses (EAB docs/DEVICE-AUTH.md, docs/personal-access-tokens.md):
//   POST   /api/auth/device/code    -> { data: { deviceCode, userCode, verificationUri, verificationUriComplete, expiresIn, interval } }
//   POST   /api/auth/device/token   -> 400 { error: authorization_pending | slow_down | expired_token | access_denied }
//                                      or 200 { data: { accessToken: "pat_...", credentialType: "pat", expiresAt, tokenId, ... } }
//   GET    /api/auth/me             -> { data: { id, email, displayName, ... } }   (bearer required)
//   DELETE /api/pat/:id             -> 204                                        (bearer required)
//   GET    /api/localai/nodes       -> held open while `holdNodes` is set (to prove an abort), else { data: { summary, nodes } }
//   GET    /api/localai/nodes/:id   -> the card plus `lastState.models` (the Lab's Models panel)
//   POST   /api/localai/nodes/:id/commands  -> 201 { data: CommandView } (queued), or the scripted refusal (`refuseCommands`)
//   GET    /api/localai/commands/:id        -> queued, then running, then succeeded, one step per read (`holdCommand` stops it)
//   GET    /api/localai/alerts      -> { data: { alerts: [] } }
//   `down` destroys every connection (a network error); `nodesStatus` answers the node list with that status (a 5xx).
// The app's code never talks to a real host in a test: with LOCALAI_DESKTOP_TEST_CLOUD_PORT the main process connects
// `ai.marin.cr:443` to this server on a loopback port, in plain HTTP (cloud-link.ts `createLoopbackDispatcher`; honoured only
// by an unpackaged run and the e2e test copy). The unit tests of the device flow use their own scripted fakes.
import { randomBytes, randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { type IncomingMessage, type ServerResponse, createServer } from 'node:http';
import { join } from 'node:path';
import type { AddressInfo } from 'node:net';

export const FAKE_CLOUD_HOST = 'ai.marin.cr' as const;

/** `GET /api/localai/nodes` of the cloud's own 3-node fixture (apps/cloud/apps/api/test/fixtures/localai/lab-3-nodes.json), a copy. */
export const LAB_3_NODES = JSON.parse(readFileSync(join(__dirname, 'lab-3-nodes.json'), 'utf8')) as { summary: Record<string, number>; nodes: Array<Record<string, unknown>> };

/** The models `lastState.models` of every node in the fake. */
export const FAKE_MODELS = [
  { id: 'qwen3.6-35b-a3b-q4km', present: true, verified: true, loadable: true, loadable_reason: null, status: 'loaded', default: true },
  { id: 'qwen3.5-9b-q4km', present: true, verified: true, loadable: true, loadable_reason: null, status: 'unloaded', default: false },
  { id: 'big-model-q8', present: false, verified: false, loadable: false, loadable_reason: 'not downloaded', status: 'unloaded', default: false },
];

export interface FakeCommand {
  id: string;
  nodeId: string;
  type: string;
  params: unknown;
  status: 'queued' | 'running' | 'succeeded' | 'failed';
  reads: number;
}

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
  /** Destroy every connection at once: the cloud is unreachable (a network error). */
  down: boolean;
  /** Answer `GET /api/localai/nodes` with this status (503: a 5xx); 200 by default. */
  nodesStatus: number;
  /** Refuse every command POST with this status and body (403, 409 LAI-CLD-035, 422 LAI-CLD-034 ...); null accepts. */
  refuseCommands: { status: number; body: unknown } | null;
  /** Keep the commands `queued`: a read of one does not advance it. */
  holdCommand: boolean;
  /** The cards `GET /api/localai/nodes` answers (the 3-node fixture by default). */
  nodes: Array<Record<string, unknown>>;
  /** Commands accepted so far. */
  readonly commands: FakeCommand[];
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
  const commands: FakeCommand[] = [];
  const flags = { holdNodes: false, holdMe: false, rejectBearer: false, down: false, nodesStatus: 200, holdCommand: false, refuseCommands: null as FakeCloud['refuseCommands'], nodes: structuredClone(LAB_3_NODES.nodes), displayName: options.displayName === undefined ? 'Person A' : options.displayName };

  const send = (res: ServerResponse, status: number, body?: string): void => {
    res.writeHead(status, { 'content-type': 'application/json', connection: 'close' });
    res.end(body);
  };

  const server = createServer((req, res) => {
    if (flags.down) return void req.socket.destroy();
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
        if (flags.nodesStatus !== 200) return send(res, flags.nodesStatus, JSON.stringify({ statusCode: flags.nodesStatus, message: 'Service Unavailable' }));
        return send(res, 200, envelope({ summary: { ...LAB_3_NODES.summary, nodes: flags.nodes.length }, nodes: flags.nodes }));
      }
      const node = /^\/api\/localai\/nodes\/(ai-n[1-9][0-9]{0,3})$/.exec(url.pathname);
      if (method === 'GET' && node) {
        if (bearer === undefined) return send(res, 401, '{}');
        const card = flags.nodes.find((n) => n['nodeId'] === node[1]);
        if (!card) return send(res, 404, JSON.stringify({ statusCode: 404, message: 'No Local AI node has this id' }));
        return send(res, 200, envelope({ ...card, lastState: { models: FAKE_MODELS }, keyReceived: true }));
      }
      const post = /^\/api\/localai\/nodes\/(ai-n[1-9][0-9]{0,3})\/commands$/.exec(url.pathname);
      if (method === 'POST' && post) {
        if (bearer === undefined) return send(res, 401, '{}');
        if (flags.refuseCommands) return send(res, flags.refuseCommands.status, JSON.stringify(flags.refuseCommands.body));
        const asked = body as { type?: unknown; params?: unknown } | null;
        const command: FakeCommand = { id: randomUUID(), nodeId: post[1]!, type: `localai.${String(asked?.type)}`, params: asked?.params ?? {}, status: 'queued', reads: 0 };
        commands.push(command);
        return send(res, 201, envelope(commandView(command)));
      }
      const read = /^\/api\/localai\/commands\/([0-9a-f-]{36})$/.exec(url.pathname);
      if (method === 'GET' && read) {
        if (bearer === undefined) return send(res, 401, '{}');
        const command = commands.find((c) => c.id === read[1]);
        if (!command) return send(res, 404, JSON.stringify({ statusCode: 404, message: 'No such command' }));
        if (!flags.holdCommand) {
          command.reads += 1;
          command.status = command.reads === 1 ? 'running' : 'succeeded';
        }
        return send(res, 200, envelope({ ...commandView(command), result: command.status === 'succeeded' ? {} : undefined }));
      }
      if (method === 'GET' && url.pathname === '/api/localai/alerts') {
        if (bearer === undefined) return send(res, 401, '{}');
        return send(res, 200, envelope({ alerts: [] }));
      }
      return send(res, 404, JSON.stringify({ statusCode: 404, message: 'Not Found' }));
    })().catch(() => send(res, 500));
  });

  const commandView = (c: FakeCommand): Record<string, unknown> => ({
    id: c.id,
    nodeId: c.nodeId,
    type: c.type,
    status: c.status,
    params: c.params,
    source: 'desktop',
    requestedById: null,
    attempts: 0,
    expiresAt: new Date(Date.now() + 900_000).toISOString(),
    createdAt: new Date().toISOString(),
    claimedAt: null,
    finishedAt: null,
    error: null,
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
    get down() {
      return flags.down;
    },
    set down(value: boolean) {
      flags.down = value;
    },
    get nodesStatus() {
      return flags.nodesStatus;
    },
    set nodesStatus(value: number) {
      flags.nodesStatus = value;
    },
    get refuseCommands() {
      return flags.refuseCommands;
    },
    set refuseCommands(value: FakeCloud['refuseCommands']) {
      flags.refuseCommands = value;
    },
    get holdCommand() {
      return flags.holdCommand;
    },
    set holdCommand(value: boolean) {
      flags.holdCommand = value;
    },
    get nodes() {
      return flags.nodes;
    },
    set nodes(value: Array<Record<string, unknown>>) {
      flags.nodes = value;
    },
    commands,
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
