// A lab for the STREAM specs: the real gateway scope listening on a real port (an `inject()` call buffers the whole body, so
// it cannot show whether events arrive one by one), the real `GatewayService`, `StreamProxy`, `AttemptLoop`, `UpstreamClient`
// and `RoutingTable` over in-memory data, and fake llama-servers that are real HTTP servers. Only the key store, the model
// resolver and the settings are fakes. `StreamClient` is a plain `http.request` that timestamps every event it receives.
import { randomBytes } from 'node:crypto';
import { request as httpRequest, type ClientRequest, type IncomingHttpHeaders, type IncomingMessage } from 'node:http';
import type { AddressInfo } from 'node:net';

import { AttemptLoop } from '../../../src/localai/gateway/attempts';
import { GatewayError } from '../../../src/localai/gateway/errors';
import { GatewayService } from '../../../src/localai/gateway/gateway.service';
import { buildGateway, type TestGateway } from '../../../src/localai/gateway/gateway.testkit';
import { proxyHandlers } from '../../../src/localai/gateway/handlers';
import type { AdmissionInput, AdmissionTicket, GatewayLimits, SettleInfo } from '../../../src/localai/gateway/limits';
import { DEFAULT_MODEL_POLICY, type GatewayModelResolver, type ResolvedGatewayModel } from '../../../src/localai/gateway/model-resolver';
import { keyAllows } from '../../../src/localai/gateway/models/resolve';
import { DEFAULT_GATEWAY_TIMEOUTS, type ProxySettings, type ProxySettingsReader } from '../../../src/localai/gateway/proxy-settings';
import type { SourceNodeRow } from '../../../src/localai/gateway/routing/build-candidates';
import { FakeBus, FakeClock, model, node } from '../../../src/localai/gateway/routing/routing.fixtures';
import { RoutingTable } from '../../../src/localai/gateway/routing/routing-table';
import { SseSplitter } from '../../../src/localai/gateway/stream/sse-splitter';
import { StreamProxy, type StreamProxyOptions } from '../../../src/localai/gateway/stream/stream-proxy';
import { UpstreamClient } from '../../../src/localai/gateway/upstream/client';
import type { UsageRecord, UsageSink } from '../../../src/localai/gateway/usage/usage-sink';
import { FakeLlama, okBehaviour, type FakeLlamaBehaviour } from '../../fakes/fake-llama';

class StreamResolver implements GatewayModelResolver {
  async resolve(requested: string, key: { allowedModels: string[] }): Promise<ResolvedGatewayModel> {
    const modelId = requested === 'lab-model' ? 'model-a' : requested;
    if (modelId !== 'model-a') throw new GatewayError('LAI-GW-020');
    if (!keyAllows(key, requested, { id: modelId })) throw new GatewayError('LAI-GW-012');
    return { modelId, requestedModel: requested, policy: DEFAULT_MODEL_POLICY, roles: ['chat'], inApprovedCatalog: true };
  }
}

export class RecordingSink implements UsageSink {
  records: UsageRecord[] = [];
  record(record: UsageRecord): void {
    this.records.push(record);
  }
}

export class RecordingLimits implements GatewayLimits {
  admitted: AdmissionInput[] = [];
  settled: SettleInfo[] = [];
  async admit(input: AdmissionInput): Promise<AdmissionTicket> {
    this.admitted.push(input);
    return { headers: () => ({ 'x-ratelimit-limit-requests': '60' }), settle: (info) => void this.settled.push(info) };
  }
}

export const CHAT = { model: 'model-a', stream: true, messages: [{ role: 'user', content: 'Hola' }] };

export interface StreamLab {
  gw: TestGateway;
  table: RoutingTable;
  sink: RecordingSink;
  limits: RecordingLimits;
  settings: ProxySettings;
  llamas: Record<string, FakeLlama>;
  /** `http://127.0.0.1:<port>` of the gateway's own listener. */
  origin: string;
  /** Opens a streaming request to the gateway (path under `/api/gateway/v1`). */
  open(path: string, body: unknown, options?: OpenOptions): StreamClient;
  cleanup(): Promise<void>;
}

export interface LabNode {
  id: string;
  behaviour?: FakeLlamaBehaviour;
}

export async function streamLab(
  nodes: LabNode[],
  tune: (settings: ProxySettings) => void = () => undefined,
  proxyOptions: StreamProxyOptions = {},
): Promise<StreamLab> {
  const llamas: Record<string, FakeLlama> = {};
  const origins = new Map<string, string>();
  const nodeKeys = new Map<string, string>();
  const rows: SourceNodeRow[] = [];
  for (const [index, entry] of nodes.entries()) {
    const llama = await FakeLlama.start(entry.behaviour ?? okBehaviour);
    llamas[entry.id] = llama;
    origins.set(`${entry.id}.example.test`, llama.origin);
    nodeKeys.set(entry.id, `llama-${randomBytes(16).toString('hex')}`);
    rows.push(node(entry.id, [model('model-a')], { gatewayPriority: 10 + index }));
  }
  const table = new RoutingTable({ load: async () => ({ rows, settings: { queueFactor: 4 } }) }, new FakeBus() as never, new FakeClock(), { timers: false, rebuildIntervalMs: 10_000 });
  table.onModuleInit();
  await table.rebuild();

  const client = new UpstreamClient({ version: 'test', resolveOrigin: (host) => origins.get(host) ?? 'http://127.0.0.1:1' });
  const settings: ProxySettings = { timeouts: { ...DEFAULT_GATEWAY_TIMEOUTS, connectMs: 500, firstByteMs: 3000, idleMs: 3000, totalMs: 8000, nonStreamTotalMs: 8000 }, maxAttempts: 3 };
  tune(settings);
  const settingsReader: ProxySettingsReader = { read: async () => settings };
  const sink = new RecordingSink();
  const limits = new RecordingLimits();
  const loop = new AttemptLoop({ routing: table, keys: { get: async (id) => nodeKeys.get(id) ?? null }, transport: client, log: { warn: () => undefined } });
  const service = new GatewayService(new StreamResolver(), limits, sink, new StreamProxy(proxyOptions), settingsReader, loop);
  const gw = await buildGateway({ handlers: proxyHandlers(service) });
  await gw.app.listen({ port: 0, host: '127.0.0.1' });
  const origin = `http://127.0.0.1:${(gw.app.server.address() as AddressInfo).port}`;

  return {
    gw,
    table,
    sink,
    limits,
    settings,
    llamas,
    origin,
    open: (path, body, options) => new StreamClient(origin, `${gw.prefix}${path}`, gw.token, body, options),
    async cleanup() {
      await gw.app.close();
      await client.close();
      table.onModuleDestroy();
      await Promise.all(Object.values(llamas).map((llama) => llama.close()));
    },
  };
}

export interface OpenOptions {
  headers?: Record<string, string>;
  /** Stop reading the response right after the head (the socket fills up, the gateway sees a slow client). */
  pauseAfterHead?: boolean;
  token?: string;
  /** Keep the received events and bytes (default). Off for a very large body: only the counters are kept. */
  retain?: boolean;
}

export interface ReceivedEvent {
  at: number;
  text: string;
}

/** One streaming request. Every complete SSE event is timestamped when it arrives. */
export class StreamClient {
  status = 0;
  headers: IncomingHttpHeaders = {};
  events: ReceivedEvent[] = [];
  eventCount = 0;
  bytes = 0;
  private readonly received: Buffer[] = [];
  ended = false;
  closed = false;
  /** Resolves when the response head arrived. */
  readonly head: Promise<void>;
  /** Resolves when the response ended or the connection closed. */
  readonly done: Promise<void>;
  private readonly splitter = new SseSplitter();
  private req!: ClientRequest;
  private res: IncomingMessage | null = null;

  constructor(origin: string, path: string, token: string, body: unknown, options: OpenOptions = {}) {
    let headResolve!: () => void;
    let doneResolve!: () => void;
    this.head = new Promise((resolve) => (headResolve = resolve));
    this.done = new Promise((resolve) => (doneResolve = resolve));
    const payload = typeof body === 'string' ? body : JSON.stringify(body);
    const url = new URL(path, origin);
    this.req = httpRequest(
      url,
      { method: 'POST', agent: false, headers: { authorization: `Bearer ${options.token ?? token}`, 'content-type': 'application/json', 'content-length': Buffer.byteLength(payload), ...options.headers } },
      (res) => {
        this.res = res;
        this.status = res.statusCode ?? 0;
        this.headers = res.headers;
        if (options.pauseAfterHead) res.pause();
        const retain = options.retain !== false;
        res.on('data', (chunk: Buffer) => {
          this.bytes += chunk.length;
          const at = performance.now();
          if (retain) this.received.push(chunk);
          for (const event of this.splitter.push(chunk)) {
            this.eventCount += 1;
            if (retain) this.events.push({ at, text: event.toString('utf8') });
          }
        });
        res.on('end', () => {
          this.ended = true;
        });
        res.on('close', () => {
          this.closed = true;
          doneResolve();
        });
        headResolve();
      },
    );
    this.req.on('error', () => {
      this.closed = true;
      headResolve();
      doneResolve();
    });
    this.req.end(payload);
  }

  /** Everything received, as text (also a JSON error body, which is not made of events). */
  get body(): string {
    return Buffer.concat(this.received).toString('utf8');
  }

  get json(): any {
    try {
      return JSON.parse(this.body);
    } catch {
      return null;
    }
  }

  get texts(): string[] {
    return this.events.map((event) => event.text);
  }

  /** The whole body as received (events only: a partial tail is not counted). */
  get text(): string {
    return this.texts.join('');
  }

  /** The decoded `data:` payloads, `[DONE]` as the string. */
  get payloads(): string[] {
    return this.texts.map((text) => text.replace(/^data: ?/, '').trim());
  }

  pause(): void {
    this.res?.pause();
  }

  resume(): void {
    this.res?.resume();
  }

  /** Hangs up like a client that closed its tab. */
  abort(): void {
    this.req.destroy();
    this.res?.destroy();
  }
}

export const waitFor = async (condition: () => boolean, ms = 3000, step = 5): Promise<boolean> => {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    if (condition()) return true;
    await new Promise((resolve) => setTimeout(resolve, step));
  }
  return condition();
};
