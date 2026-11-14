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
  /** Every `POST /api/v1/mode` and `POST /api/v1/mode/survival` the fake saw: the body, the bearer and the PIN grant header. */
  readonly modeRequests: Array<{ path: string; body: unknown; authorization: string | undefined; pinGrant: string | undefined; contentType: string | undefined }>;
  /** The llama API key `GET /key` returns (64 hex). A rotation replaces it. */
  apiKey: string;
  /** Every `GET /api/v1/key` the fake saw (the agent audits each one as `key.show`). */
  readonly keyRequests: Array<{ authorization: string | undefined }>;
  /** Every `POST /api/v1/key/rotate`: the body, the bearer, the PIN grant header and whether it was accepted. */
  readonly keyRotations: Array<{ body: unknown; authorization: string | undefined; pinGrant: string | undefined; accepted: boolean }>;
  /** The picture `GET /api/v1/ensure` answers (ADR-0010); a `POST` or `DELETE` replaces it. Default: Needs you, one pending step. */
  ensureView: Record<string, unknown>;
  /** Every `/api/v1/ensure` call: method, body, bearer, PIN grant header, `Accept-Language`, and whether a `confirm` was accepted. */
  readonly ensureRequests: Array<{ method: string; body: unknown; authorization: string | undefined; pinGrant: string | undefined; language: string | undefined; accepted: boolean }>;
  /** Answer the next `POST /api/v1/ensure` with this error (then back to accepting). */
  failNextEnsure: { status: number; code: string; message: string; fix: string } | null;
  /** The admin PIN `POST /pin/verify` accepts; null: no PIN is set (409 LAI-SEC-005). */
  pin: string | null;
  /** Wrong PINs counted so far in the 5-minute window; at `maxPinFailures` the fake answers 429 LAI-SEC-004. */
  pinFailures: number;
  /** Every `POST /api/v1/pin/verify`: whether it was refused for a lockout, and how it ended (the PIN itself is not kept). */
  readonly pinVerifications: Array<{ outcome: 'ok' | 'wrong' | 'locked' | 'no-pin' | 'bad-request'; authorization: string | undefined }>;
  /** Alerts reported by /status. */
  alerts: Array<{ code: string; severity: string; title: string; fix: string }>;
  /** Connectivity reported by /status and /mode. */
  connectivity: { value: string; none_since: string | null };
  /** `suggest_survival` of GET /mode. */
  suggestSurvival: boolean;
  /** Models reported by /status as loaded, and the default one. */
  models: { default: string | null; loaded: string[] };
  /**
   * Issue #515: what `GET /api/v1/music/status` says. `null` (the default): the route does not exist (404, an agent from
   * before E11). The `/music/` page and a song's audio are always served.
   */
  music: { installed: boolean } | null;
  /** Answer the next mode change with this error instead of accepting it (then back to accepting). */
  failNextModeChange: { status: number; code: string; details?: Record<string, unknown> } | null;
  /** The PIN grant the fake accepts for leaving survival. Default: none is valid. */
  validPinGrant: string | null;
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

/** Issue #515: the fake Music page (a sample picker, a song link) and a 44-byte WAV header with no samples. */
export const MUSIC_HTML = `<!doctype html><meta charset="utf-8"><title>/music/</title><h1>/music/</h1>
<input id="sample" type="file" accept="audio/*">
<a id="song" href="/api/v1/music/songs/sng_1/audio" download>Download</a>`;
export const FAKE_WAV = Buffer.from('UklGRiQAAABXQVZFZm10IBAAAAABAAEAgLsAAAB3AQACABAAZGF0YQAAAAA=', 'base64');

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
  const modeRequests: FakeAgent['modeRequests'] = [];
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
    alerts: [] as FakeAgent['alerts'],
    connectivity: { value: 'not_checked', none_since: null } as FakeAgent['connectivity'],
    suggestSurvival: false,
    models: { default: null, loaded: [] } as FakeAgent['models'],
    music: null as FakeAgent['music'],
    failNextModeChange: null as FakeAgent['failNextModeChange'],
    validPinGrant: null as string | null,
    apiKey: randomBytes(32).toString('hex'),
    pin: null as string | null,
    pinFailures: 0,
  };
  const MAX_PIN_FAILURES = 5;
  const ensureStep = (id: string, status: string, over: Record<string, unknown> = {}): Record<string, unknown> => ({
    id,
    kind: id.startsWith('tool.') ? 'install_tool' : id === 'whisper.build' ? 'build_whisper' : 'restart_service',
    status,
    needs_confirmation: status === 'needs_confirm',
    title: id === 'tool.piper' ? 'Voice program (piper)' : id === 'whisper.build' ? 'Build the speech program' : id,
    detail: 'From the rebuild kit.',
    ...over,
  });
  const ensureView = (stateName: string, steps: Array<Record<string, unknown>>): Record<string, unknown> => ({
    state: stateName,
    auto: 'on',
    message: stateName === 'complete' ? 'Complete: this computer has everything it needs.' : stateName === 'installing' ? `Installing what's missing (1 of ${steps.length})` : 'Needs you: the voice program is missing.',
    next_action: stateName === 'needs_you' ? { kind: 'press_button', text: "Press Install what's missing." } : { kind: stateName === 'installing' ? 'wait' : 'none', text: '' },
    progress: { done: 0, total: steps.length },
    steps,
    run: null,
    dry_run: false,
    checked_at: '2026-11-10T10:00:00Z',
  });
  const ensureState = { view: ensureView('needs_you', [ensureStep('tool.piper', 'pending')]), fail: null as FakeAgent['failNextEnsure'] };
  const ensureRequests: FakeAgent['ensureRequests'] = [];
  const keyRequests: FakeAgent['keyRequests'] = [];
  const keyRotations: FakeAgent['keyRotations'] = [];
  const pinVerifications: FakeAgent['pinVerifications'] = [];
  const readBody = (req: IncomingMessage, then: (body: unknown) => void): void => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk: Buffer) => chunks.push(chunk));
    req.on('end', () => {
      let body: unknown = null;
      try {
        body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      } catch {
        // recorded as null
      }
      then(body);
    });
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
    connectivity: { value: state.connectivity.value, checked_at: null, none_since: state.connectivity.none_since },
    services: [],
    models: { default: state.models.default, loaded: state.models.loaded, loading: [], present: 0, loadable: 0, models_max: 1 },
    cloud: { enrolled: false, state: 'unenrolled', url: null, last_heartbeat_at: null },
    alerts: state.alerts,
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
    // Issue #515: the Music page: a sample picker and a link that downloads a song.
    if (method === 'GET' && path === '/music/') {
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
      res.end(MUSIC_HTML);
      return;
    }
    if (method === 'GET' && /^\/api\/v1\/music\/songs\/[A-Za-z0-9_-]+\/audio$/.test(path)) {
      // A real agent wants the session; the fake only needs to send bytes as an attachment.
      res.writeHead(200, { 'content-type': 'audio/wav', 'content-disposition': 'attachment; filename="song-1.wav"', 'content-length': String(FAKE_WAV.length) });
      res.end(FAKE_WAV);
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

    if (method === 'GET' && path === '/api/v1/mode') {
      if (credential(req) === null) {
        error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
        return;
      }
      json(res, 200, {
        mode: state.mode,
        since: null,
        transition: null,
        allowed_targets: ['online', 'offline', 'survival'].filter((m) => m !== state.mode),
        leave_requires_admin: state.mode === 'survival',
        connectivity: state.connectivity.value,
        suggest_survival: state.suggestSurvival,
      });
      return;
    }

    if (method === 'POST' && (path === '/api/v1/mode' || path === '/api/v1/mode/survival')) {
      const chunks: Buffer[] = [];
      req.on('data', (chunk: Buffer) => chunks.push(chunk));
      req.on('end', () => {
        let body: unknown = null;
        try {
          body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        } catch {
          // recorded as null
        }
        const pinGrant = req.headers['x-localai-pin-grant'];
        modeRequests.push({
          path,
          body,
          authorization: req.headers.authorization,
          pinGrant: Array.isArray(pinGrant) ? pinGrant[0] : pinGrant,
          contentType: req.headers['content-type'],
        });
        if (!(req.headers['content-type'] ?? '').startsWith('application/json')) {
          error(res, 415, 'LAI-AGT-020', 'Content-Type must be application/json');
          return;
        }
        const target = path === '/api/v1/mode/survival' ? 'survival' : (body as { target?: string } | null)?.target;
        // /mode/survival needs no credential; /mode needs an operator.
        if (path === '/api/v1/mode' && credential(req) === null) {
          error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
          return;
        }
        if (target !== 'online' && target !== 'offline' && target !== 'survival') {
          error(res, 400, 'LAI-AGT-020', 'Unknown target');
          return;
        }
        const failure = path === '/api/v1/mode' ? state.failNextModeChange : null;
        if (failure) {
          state.failNextModeChange = null;
          json(res, failure.status, { error: { code: failure.code, message: 'Another operation is already running', fix: 'Wait for it to finish, then try again.', details: failure.details ?? {} } });
          return;
        }
        const pin = req.headers['x-localai-pin-grant'];
        if (path === '/api/v1/mode' && state.mode === 'survival' && (state.validPinGrant === null || pin !== state.validPinGrant)) {
          json(res, 403, { error: { code: 'LAI-SEC-002', message: 'This action needs the admin PIN', fix: 'Enter the admin PIN when asked.', details: { pin_required: true } } });
          return;
        }
        if (state.mode === target) {
          json(res, 200, { mode: target, already: true });
          return;
        }
        const from = state.mode;
        state.mode = target;
        publish('mode.changed', { from, to: target, op_id: 'op_fake', warnings: [] });
        json(res, 202, target === 'survival' ? { op_id: 'op_fake', target } : { op_id: 'op_fake', from, target, transition: { step: 'link_stop', started_at: new Date().toISOString() } });
      });
      return;
    }

    if (method === 'GET' && path === '/api/v1/key') {
      if (credential(req) === null) {
        error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
        return;
      }
      keyRequests.push({ authorization: req.headers.authorization });
      const key = state.apiKey;
      json(
        res,
        200,
        {
          key,
          fingerprint: `${key.slice(0, 4)}…${key.slice(-4)}`,
          base_urls: { local: 'http://127.0.0.1:8080/v1', tailnet: null, public: 'https://ai-n3.example.test/v1' },
          models: ['model-a', 'model-b'],
          snippets: {
            curl: 'curl -s http://127.0.0.1:8080/v1/chat/completions -H "Authorization: Bearer $KEY" -H \'Content-Type: application/json\' -d \'{"model":"model-a","messages":[{"role":"user","content":"Hello"}]}\'',
            env: 'OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=<key>',
            opencode: {
              provider: {
                'local-llama': {
                  npm: '@ai-sdk/openai-compatible',
                  options: { baseURL: 'http://127.0.0.1:8080/v1', apiKey: '{file:~/.config/opencode/llama-api-key}' },
                  models: { 'model-a': { limit: { context: 32768, output: 8192 } } },
                },
              },
            },
            openai_python: 'from openai import OpenAI\nclient = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key=KEY)',
          },
        },
        { 'cache-control': 'no-store' },
      );
      return;
    }

    if (method === 'POST' && path === '/api/v1/pin/verify') {
      readBody(req, (body) => {
        const authorization = req.headers.authorization;
        if (credential(req) === null) {
          error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
          return;
        }
        const given = (body as { pin?: unknown } | null)?.pin;
        if (typeof given !== 'string') {
          pinVerifications.push({ outcome: 'bad-request', authorization });
          error(res, 400, 'LAI-AGT-020', 'pin must be a string');
          return;
        }
        if (state.pin === null) {
          pinVerifications.push({ outcome: 'no-pin', authorization });
          json(res, 409, { error: { code: 'LAI-SEC-005', message: 'No admin PIN has been set', fix: 'Run sudo aictl pin set', details: {} } });
          return;
        }
        if (state.pinFailures >= MAX_PIN_FAILURES) {
          pinVerifications.push({ outcome: 'locked', authorization });
          json(res, 429, { error: { code: 'LAI-SEC-004', message: 'Too many wrong tries', fix: 'Wait 5 minutes.', details: { retry_after_s: 300, locked: false } } }, { 'retry-after': '300', 'cache-control': 'no-store' });
          return;
        }
        if (given !== state.pin) {
          state.pinFailures += 1;
          pinVerifications.push({ outcome: 'wrong', authorization });
          json(res, 401, { error: { code: 'LAI-SEC-003', message: 'Wrong PIN', fix: 'Try again.', details: { remaining_attempts: MAX_PIN_FAILURES - state.pinFailures } } }, { 'cache-control': 'no-store' });
          return;
        }
        pinVerifications.push({ outcome: 'ok', authorization });
        const grant = `pg_${randomBytes(32).toString('base64url')}`;
        state.validPinGrant = grant;
        json(res, 200, { grant, expires_at: new Date(Date.now() + 300_000).toISOString().replace(/\.\d+Z$/, 'Z') }, { 'cache-control': 'no-store' });
      });
      return;
    }

    if (method === 'POST' && path === '/api/v1/key/rotate') {
      readBody(req, (body) => {
        const pinGrant = req.headers['x-localai-pin-grant'];
        const grant = Array.isArray(pinGrant) ? pinGrant[0] : pinGrant;
        const accepted = credential(req) !== null && state.validPinGrant !== null && grant === state.validPinGrant;
        keyRotations.push({ body, authorization: req.headers.authorization, pinGrant: grant, accepted });
        if (credential(req) === null) {
          error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
          return;
        }
        if (!accepted) {
          json(res, 403, { error: { code: 'LAI-SEC-002', message: 'This action needs the admin PIN', fix: 'Enter the admin PIN when asked.', details: { pin_required: true } } });
          return;
        }
        state.apiKey = randomBytes(32).toString('hex');
        json(res, 200, {
          fingerprint: `${state.apiKey.slice(0, 4)}…${state.apiKey.slice(-4)}`,
          rotated_at: new Date().toISOString().replace(/\.\d+Z$/, 'Z'),
          cloud_synced: false,
          restored_models: [],
          clients_to_update: ['OpenCode on this computer: ~/.config/opencode/llama-api-key', 'Any script that uses the old key directly'],
          op_id: 'op_fake_rotate',
        });
      });
      return;
    }

    if (path === '/api/v1/ensure' && (method === 'GET' || method === 'POST' || method === 'DELETE')) {
      readBody(req, (body) => {
        const pinGrant = req.headers['x-localai-pin-grant'];
        const grant = Array.isArray(pinGrant) ? pinGrant[0] : pinGrant;
        const language = req.headers['accept-language'];
        const confirm = (body as { confirm?: unknown } | null)?.confirm;
        const confirming = Array.isArray(confirm) && confirm.length > 0;
        const accepted = credential(req) !== null && (!confirming || (state.validPinGrant !== null && grant === state.validPinGrant));
        ensureRequests.push({ method, body, authorization: req.headers.authorization, pinGrant: grant, language: Array.isArray(language) ? language[0] : language, accepted });
        if (credential(req) === null) {
          error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
          return;
        }
        if (method === 'GET') {
          json(res, 200, ensureState.view);
          return;
        }
        if (method === 'DELETE') {
          ensureState.view = ensureView('needs_you', [ensureStep('tool.piper', 'cancelled')]);
          json(res, 202, ensureState.view);
          return;
        }
        if (ensureState.fail) {
          const { status, code, message, fix } = ensureState.fail;
          ensureState.fail = null;
          json(res, status, { error: { code, message, fix, details: {} } });
          return;
        }
        if (confirming && !accepted) {
          json(res, 403, { error: { code: 'LAI-SEC-002', message: 'This action needs the admin PIN', fix: 'Enter the admin PIN when asked.', details: { pin_required: true, reason: 'confirm' } } });
          return;
        }
        ensureState.view = ensureView('installing', [ensureStep('tool.piper', 'running')]);
        json(res, 202, ensureState.view);
      });
      return;
    }

    if (method === 'GET' && path === '/api/v1/music/status') {
      if (credential(req) === null) {
        error(res, 401, 'LAI-SEC-001', 'Missing or invalid credentials');
        return;
      }
      if (state.music === null) {
        error(res, 404, 'LAI-AGT-021', 'Unknown route');
        return;
      }
      json(res, 200, { installed: state.music.installed, loaded_model: null, engine_state: 'stopped', queue: { running: 0, queued: 0, max: 4 } });
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
    modeRequests,
    eventConnections,
    keyRequests,
    keyRotations,
    pinVerifications,
    ensureRequests,
    get ensureView() {
      return ensureState.view;
    },
    set ensureView(value: Record<string, unknown>) {
      ensureState.view = value;
    },
    get failNextEnsure() {
      return ensureState.fail;
    },
    set failNextEnsure(value: FakeAgent['failNextEnsure']) {
      ensureState.fail = value;
    },
    get apiKey() {
      return state.apiKey;
    },
    set apiKey(value: string) {
      state.apiKey = value;
    },
    get pin() {
      return state.pin;
    },
    set pin(value: string | null) {
      state.pin = value;
    },
    get pinFailures() {
      return state.pinFailures;
    },
    set pinFailures(value: number) {
      state.pinFailures = value;
    },
    get alerts() {
      return state.alerts;
    },
    set alerts(value: FakeAgent['alerts']) {
      state.alerts = value;
    },
    get connectivity() {
      return state.connectivity;
    },
    set connectivity(value: FakeAgent['connectivity']) {
      state.connectivity = value;
    },
    get suggestSurvival() {
      return state.suggestSurvival;
    },
    set suggestSurvival(value: boolean) {
      state.suggestSurvival = value;
    },
    get models() {
      return state.models;
    },
    get music() {
      return state.music;
    },
    set music(value: FakeAgent['music']) {
      state.music = value;
    },
    set models(value: FakeAgent['models']) {
      state.models = value;
    },
    get failNextModeChange() {
      return state.failNextModeChange;
    },
    set failNextModeChange(value: FakeAgent['failNextModeChange']) {
      state.failNextModeChange = value;
    },
    get validPinGrant() {
      return state.validPinGrant;
    },
    set validPinGrant(value: string | null) {
      state.validPinGrant = value;
    },
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
