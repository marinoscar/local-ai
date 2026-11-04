// A fake of the node agent for the desktop tests (docs/specs/desktop.md section 9): an HTTP server on 127.0.0.1:7070
// that answers like the real agent (agent/api/openapi.yaml): /health, /session, /status, /events (SSE with
// Last-Event-ID replay), the Host and Origin checks of agent.md 3.9.1, and a small agent UI at / and /portal.
// Test switches: a rotatable token, `expireSessions()`, `rejectLogin`, `unauthorizedAll` (the "401 switch") and
// `emit()` / `dropEventStreams()` for the event stream. Test-only; never packaged.
import { createHash, randomBytes } from 'node:crypto';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import type { IncomingMessage, Server, ServerResponse } from 'node:http';

export interface FakeAgentOptions {
  /** Default 7070, the agent's port. Pass 0 to let the OS pick a free one (unit tests). */
  port?: number;
  /** Always loopback: the real agent listens on loopback only. */
  host?: '127.0.0.1';
  mode?: string;
  version?: string;
  starting?: boolean;
  /** Keepalive comment interval of /events, like the agent's 15 s. */
  pingMs?: number;
  /** The agent token. Default: a fresh random `lat_` token. Without one, the session and status routes accept nobody. */
  token?: string;
}

export interface FakeAgent {
  readonly url: string;
  readonly port: number;
  /** Every request the fake saw, in order (method and path only). */
  readonly requests: Array<{ method: string; path: string }>;
  /** What the egress probe page (GET /egress-probe) reported back, once it finished. */
  readonly probeResults: Array<Record<string, string>>;
  /** One entry per `POST /api/v1/session`: the bearer the caller sent (the test compares it with the token file) and whether it was accepted. */
  readonly sessionRequests: Array<{ authorization: string | undefined; accepted: boolean; origin: string | undefined }>;
  /** The token the fake accepts right now. */
  token: string;
  /** Every `GET /api/v1/events` connection: the Last-Event-ID it sent and the credential it used. */
  readonly eventConnections: Array<{ lastEventId: string | undefined; via: 'bearer' | 'cookie' }>;
  /** Mode reported by /health and /status. */
  mode: string;
  /** Health `version`. */
  version: string;
  /** Refuse `POST /api/v1/session` with 401 whatever the bearer is (the LAI-DSK-007 case). */
  rejectLogin: boolean;
  /** Answer 401 on every route that needs a credential, session cookies and bearers alike (the 401 switch). */
  unauthorizedAll: boolean;
  /** Forget every session cookie, as an agent restart would. Bearer requests keep working. */
  expireSessions(): void;
  /** Publish an event to the connected /events streams (and the replay buffer). Returns its id. */
  emit(type: string, data: unknown): number;
  /** Close every open /events stream without a `shutdown` event (a crash). */
  dropEventStreams(): void;
  /** Send `event: shutdown` and close every open /events stream (a clean stop). */
  shutdownEventStreams(): void;
  /** Number of /events streams open right now. */
  openEventStreams(): number;
  close(): Promise<void>;
}

/**
 * A page that tries the three ways a renderer can reach the internet (fetch, <img>, WebSocket) and reports how each
 * ended. It has no CSP on purpose: the test is about the egress guard, not about the agent's own policy.
 */
export const EGRESS_PROBE_HTML = `<!doctype html><meta charset="utf-8"><title>egress probe</title>
<script>
const results = {};
const done = (name, value) => {
  results[name] = value;
  if (Object.keys(results).length === 3) fetch('/egress-result', { method: 'POST', body: JSON.stringify(results) });
};
fetch('https://example.com/probe.json').then(() => done('fetch', 'ok'), () => done('fetch', 'failed'));
const img = new Image();
img.onload = () => done('img', 'ok');
img.onerror = () => done('img', 'failed');
img.src = 'https://example.com/probe.png';
const ws = new WebSocket('wss://example.com/probe');
ws.onopen = () => done('ws', 'open');
ws.onerror = () => done('ws', 'failed');
</script>`;

/** The agent UI stand-in: it is the "dashboard" and proves the session cookie works by calling an authenticated route. */
export const DASHBOARD_HTML = `<!doctype html><meta charset="utf-8"><title>Local AI dashboard</title>
<h1 id="dashboard">Local AI dashboard</h1>
<p id="node">loading</p>
<script>
fetch('/api/v1/status').then((r) => r.ok ? r.json() : Promise.reject(r.status)).then(
  (s) => { document.getElementById('node').textContent = 'node ' + s.node.id + ' is ' + s.mode.value; },
  (code) => { document.getElementById('node').textContent = 'status failed ' + code; },
);
</script>`;

export const PORTAL_HTML = '<!doctype html><meta charset="utf-8"><title>Survival Portal</title><h1 id="portal">Survival Portal</h1>';

export function newFakeToken(): string {
  return `lat_${randomBytes(32).toString('base64url')}`;
}

const sha = (value: string): string => createHash('sha256').update(value).digest('hex');

interface BufferedEvent {
  id: number;
  frame: string;
}

export async function startFakeAgent(options: FakeAgentOptions = {}): Promise<FakeAgent> {
  const host = options.host ?? '127.0.0.1';
  const requests: FakeAgent['requests'] = [];
  const probeResults: FakeAgent['probeResults'] = [];
  const sessionRequests: FakeAgent['sessionRequests'] = [];
  const eventConnections: FakeAgent['eventConnections'] = [];
  const sessions = new Set<string>();
  const streams = new Set<ServerResponse>();
  const buffer: BufferedEvent[] = [];
  let nextId = 1;
  let port = 0;

  const state = {
    token: options.token ?? newFakeToken(),
    mode: options.mode ?? 'online',
    version: options.version ?? '0.1.0',
    rejectLogin: false,
    unauthorizedAll: false,
  };

  const json = (res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}): void => {
    res.writeHead(status, { 'content-type': 'application/json', ...headers });
    res.end(JSON.stringify(body));
  };
  const error = (res: ServerResponse, status: number, code: string, message: string): void =>
    json(res, status, { error: { code, message, fix: 'Run `aictl doctor`.', details: {} } });

  /** The Host and Origin checks of agent.md 3.9.1 steps 2 and 3. Returns true when the request was refused. */
  const refuseBadHostOrOrigin = (req: IncomingMessage, res: ServerResponse): boolean => {
    const hostHeader = req.headers.host ?? '';
    if (hostHeader !== `127.0.0.1:${port}` && hostHeader !== `localhost:${port}`) {
      error(res, 403, 'LAI-SEC-006', 'Bad Host header');
      return true;
    }
    const origin = req.headers.origin;
    if (origin !== undefined && ![`http://127.0.0.1:${port}`, `http://localhost:${port}`, 'localai://app'].includes(origin)) {
      error(res, 403, 'LAI-SEC-007', 'Bad Origin');
      return true;
    }
    return false;
  };

  const cookieOf = (req: IncomingMessage, name: string): string | undefined => {
    for (const part of (req.headers.cookie ?? '').split(';')) {
      const [key, ...rest] = part.trim().split('=');
      if (key === name) return rest.join('=');
    }
    return undefined;
  };

  /** Which credential the request carries, if it is a valid one. */
  const credential = (req: IncomingMessage): 'bearer' | 'cookie' | null => {
    if (state.unauthorizedAll) return null;
    const auth = req.headers.authorization;
    if (auth !== undefined && auth === `Bearer ${state.token}`) return 'bearer';
    const cookie = cookieOf(req, 'localai_session');
    if (cookie !== undefined && sessions.has(sha(cookie))) return 'cookie';
    return null;
  };

  const statusBody = (): unknown => ({
    node: { id: 'ai-n3', display_name: 'Fake node (ai-n3)', hostname: 'fake-host', agent_version: state.version, uptime_s: 1 },
    mode: { value: state.mode, since: null, transition: null },
    connectivity: { value: 'not_checked', checked_at: null, none_since: null },
    services: [],
    models: { default: null, loaded: [], loading: [], present: 0, loadable: 0, models_max: 1 },
    cloud: { enrolled: false, state: 'unenrolled', url: null, last_heartbeat_at: null },
    alerts: [],
  });

  const publish = (type: string, data: unknown): number => {
    const id = nextId++;
    const envelope = { id, type, at: new Date().toISOString().replace(/\.\d+Z$/, 'Z'), node: 'ai-n3', data };
    const frame = `id: ${id}\nevent: ${type}\ndata: ${JSON.stringify(envelope)}\n\n`;
    buffer.push({ id, frame });
    if (buffer.length > 1000) buffer.shift();
    for (const stream of streams) stream.write(frame);
    return id;
  };

  const server: Server = createServer((req: IncomingMessage, res) => {
    const path = (req.url ?? '/').split('?')[0] ?? '/';
    const method = req.method ?? 'GET';
    requests.push({ method, path });

    if (method === 'GET' && path === '/egress-probe') {
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
      res.end(EGRESS_PROBE_HTML);
      return;
    }
    if (method === 'POST' && path === '/egress-result') {
      const chunks: Buffer[] = [];
      req.on('data', (chunk: Buffer) => chunks.push(chunk));
      req.on('end', () => {
        probeResults.push(JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, string>);
        res.writeHead(204);
        res.end();
      });
      return;
    }
    // The agent UI and the Portal are static and need no credential (agent.md 3.9.3).
    if (method === 'GET' && (path === '/' || path === '/portal' || path === '/portal/')) {
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
      res.end(path === '/' ? DASHBOARD_HTML : PORTAL_HTML);
      return;
    }
    if (method === 'GET' && (path === '/chat/' || path === '/library/' || path === '/maps/')) {
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
      res.end(`<!doctype html><meta charset="utf-8"><title>${path}</title><h1>${path}</h1>`);
      return;
    }

    if (!path.startsWith('/api/v1/')) {
      error(res, 404, 'LAI-AGT-021', 'Unknown route');
      return;
    }
    if (refuseBadHostOrOrigin(req, res)) return;

    if (method === 'GET' && path === '/api/v1/health') {
      json(res, 200, { ok: true, version: state.version, mode: state.mode, starting: options.starting ?? false });
      return;
    }

    if (method === 'POST' && path === '/api/v1/session') {
      const contentType = req.headers['content-type'] ?? '';
      const auth = req.headers.authorization;
      const accepted = !state.rejectLogin && !state.unauthorizedAll && auth !== undefined && auth === `Bearer ${state.token}`;
      sessionRequests.push({ authorization: auth, accepted, origin: req.headers.origin });
      req.resume();
      if (!contentType.startsWith('application/json')) {
        error(res, 415, 'LAI-AGT-020', 'Content-Type must be application/json');
        return;
      }
      if (!accepted) {
        error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
        return;
      }
      const cookie = randomBytes(32).toString('base64url');
      sessions.add(sha(cookie));
      res.writeHead(204, { 'set-cookie': `localai_session=${cookie}; Path=/; HttpOnly; SameSite=Strict; Max-Age=604800`, 'cache-control': 'no-store' });
      res.end();
      return;
    }

    if (method === 'GET' && path === '/api/v1/status') {
      if (credential(req) === null) {
        error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
        return;
      }
      json(res, 200, statusBody());
      return;
    }

    if (method === 'GET' && path === '/api/v1/events') {
      const via = credential(req);
      if (via === null) {
        error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
        return;
      }
      const lastEventId = req.headers['last-event-id'];
      eventConnections.push({ lastEventId: Array.isArray(lastEventId) ? lastEventId[0] : lastEventId, via });
      res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store', 'x-accel-buffering': 'no' });
      const last = typeof lastEventId === 'string' && /^\d+$/.test(lastEventId) ? Number(lastEventId) : null;
      if (last !== null) {
        if (last >= nextId) res.write('event: resync\ndata: {"reason":"restart"}\n\n');
        else if (buffer.length > 0 && buffer[0]!.id > last + 1) res.write('event: resync\ndata: {"reason":"gap"}\n\n');
        for (const event of buffer) if (event.id > last) res.write(event.frame);
      }
      res.write(': ping\n\n');
      streams.add(res);
      const ping = setInterval(() => res.write(': ping\n\n'), options.pingMs ?? 15_000);
      res.on('close', () => {
        clearInterval(ping);
        streams.delete(res);
      });
      return;
    }

    error(res, 404, 'LAI-AGT-021', 'Unknown route');
  });

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(options.port ?? 7070, host, resolve);
  });
  port = (server.address() as AddressInfo).port;

  const agent: FakeAgent = {
    url: `http://${host}:${port}`,
    port,
    requests,
    probeResults,
    sessionRequests,
    eventConnections,
    get token() {
      return state.token;
    },
    set token(value: string) {
      state.token = value;
    },
    get mode() {
      return state.mode;
    },
    set mode(value: string) {
      state.mode = value;
    },
    get version() {
      return state.version;
    },
    set version(value: string) {
      state.version = value;
    },
    get rejectLogin() {
      return state.rejectLogin;
    },
    set rejectLogin(value: boolean) {
      state.rejectLogin = value;
    },
    get unauthorizedAll() {
      return state.unauthorizedAll;
    },
    set unauthorizedAll(value: boolean) {
      state.unauthorizedAll = value;
    },
    expireSessions: () => sessions.clear(),
    emit: publish,
    dropEventStreams: () => {
      for (const stream of streams) stream.destroy();
      streams.clear();
    },
    shutdownEventStreams: () => {
      for (const stream of streams) stream.end('event: shutdown\ndata: {}\n\n');
      streams.clear();
    },
    openEventStreams: () => streams.size,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((err) => (err ? reject(err) : resolve()));
        server.closeAllConnections();
      }),
  };
  return agent;
}
