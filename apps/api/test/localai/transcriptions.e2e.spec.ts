// POST /v1/audio/transcriptions against REAL local HTTP servers acting as llama-servers (docs/specs/gateway.md sections
// 3.2, 3.5, 4.5 and 6): the Fastify gateway scope (fakes for auth), the real `GatewayService`, `AttemptLoop`,
// `UpstreamClient`, `RoutingTable` and multipart route. Only the key store, the model resolver and the settings are fakes.
// The tailnet cases run Pattern A (plain HTTP) to a local server; Pattern B over TLS is `tailnet-tls.spec.ts`.
import { createHash, randomBytes, randomInt } from 'node:crypto';

import { Logger } from '@nestjs/common';

import { AttemptLoop } from '../../src/localai/gateway/attempts';
import { GatewayError } from '../../src/localai/gateway/errors';
import { GatewayService } from '../../src/localai/gateway/gateway.service';
import { buildGateway, type TestGateway } from '../../src/localai/gateway/gateway.testkit';
import { DEFAULT_MODEL_POLICY, type GatewayModelResolver, type ResolvedGatewayModel } from '../../src/localai/gateway/model-resolver';
import { keyAllows } from '../../src/localai/gateway/models/resolve';
import { DEFAULT_GATEWAY_TIMEOUTS, type ProxySettings } from '../../src/localai/gateway/proxy-settings';
import type { SourceNodeRow } from '../../src/localai/gateway/routing/build-candidates';
import { FakeBus, FakeClock, model, node } from '../../src/localai/gateway/routing/routing.fixtures';
import { RoutingTable } from '../../src/localai/gateway/routing/routing-table';
import { audioCapabilityOf, transcriptionsHandlers } from '../../src/localai/gateway/transcriptions-route';
import { UpstreamClient } from '../../src/localai/gateway/upstream/client';
import { TailnetTargets } from '../../src/localai/gateway/upstream/targets';
import type { UsageRecord, UsageSink } from '../../src/localai/gateway/usage/usage-sink';
import { FakeLlama, okBehaviour, respondJson, type FakeLlamaBehaviour } from '../fakes/fake-llama';

const MIB = 1024 * 1024;
const sha256 = (data: Buffer): string => createHash('sha256').update(data).digest('hex');

// ---- multipart helpers --------------------------------------------------------------------------------------------

type Part = { name: string; value: string } | { name: string; file: { filename: string; data: Buffer; type?: string } };

function multipartBody(parts: Part[], boundary = `client-${randomBytes(6).toString('hex')}`): { payload: Buffer; contentType: string } {
  const chunks: Buffer[] = [];
  for (const part of parts) {
    if ('value' in part) {
      chunks.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${part.name}"\r\n\r\n${part.value}\r\n`));
    } else {
      chunks.push(
        Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${part.name}"; filename="${part.file.filename}"\r\nContent-Type: ${part.file.type ?? 'audio/wav'}\r\n\r\n`),
        part.file.data,
        Buffer.from('\r\n'),
      );
    }
  }
  chunks.push(Buffer.from(`--${boundary}--\r\n`));
  return { payload: Buffer.concat(chunks), contentType: `multipart/form-data; boundary=${boundary}` };
}

/** Parses an upstream multipart body well enough for the assertions: field values and the file's bytes. */
function parseMultipart(raw: Buffer, contentType: string): { fields: Array<{ name: string; value: string }>; file: { name: string; filename: string; type: string; data: Buffer } | null; boundary: string } {
  const boundary = /boundary=(.+)$/.exec(contentType)?.[1] ?? '';
  const delimiter = Buffer.from(`--${boundary}`);
  const fields: Array<{ name: string; value: string }> = [];
  let file: { name: string; filename: string; type: string; data: Buffer } | null = null;
  let position = raw.indexOf(delimiter);
  while (position !== -1) {
    const afterDelimiter = position + delimiter.length;
    if (raw.subarray(afterDelimiter, afterDelimiter + 2).toString() === '--') break;
    const headerStart = afterDelimiter + 2; // CRLF
    const headerEnd = raw.indexOf('\r\n\r\n', headerStart);
    const header = raw.subarray(headerStart, headerEnd).toString('utf8');
    const next = raw.indexOf(Buffer.concat([Buffer.from('\r\n'), delimiter]), headerEnd + 4);
    const data = raw.subarray(headerEnd + 4, next);
    const name = /name="([^"]*)"/.exec(header)?.[1] ?? '';
    const filename = /filename="([^"]*)"/.exec(header)?.[1];
    if (filename !== undefined) file = { name, filename, type: /Content-Type: (.+)/i.exec(header)?.[1]?.trim() ?? '', data };
    else fields.push({ name, value: data.toString('utf8') });
    position = next + 2;
  }
  return { fields, file, boundary };
}

// ---- the lab ------------------------------------------------------------------------------------------------------

class FakeResolver implements GatewayModelResolver {
  async resolve(requested: string, key: { allowedModels: string[] }): Promise<ResolvedGatewayModel> {
    const aliases: Record<string, string> = { 'lab-stt': 'stt-a' };
    const modelId = aliases[requested] ?? requested;
    if (!['stt-a', 'text-a'].includes(modelId)) throw new GatewayError('LAI-GW-020');
    if (!keyAllows(key, requested, { id: modelId })) throw new GatewayError('LAI-GW-012');
    return { modelId, requestedModel: requested, policy: DEFAULT_MODEL_POLICY, roles: ['chat'], inApprovedCatalog: true };
  }
}

class RecordingSink implements UsageSink {
  records: UsageRecord[] = [];
  record(record: UsageRecord): void {
    this.records.push(record);
  }
}

interface NodeSpec {
  id: string;
  behaviour?: FakeLlamaBehaviour;
  /** Modalities the node reports for `stt-a`. */
  modalities?: string[];
  /** `text-a` is served by the node as well, with no audio. */
  alsoText?: boolean;
  /** `stt-a` is not served at all (only `text-a`). */
  onlyText?: boolean;
  tailnet?: { servePattern: 'A' | 'B'; tailnetIp: string | null; tailnetMachine: string | null };
}

interface Lab {
  gw: TestGateway;
  sink: RecordingSink;
  llamas: Record<string, FakeLlama>;
  nodeKeys: Map<string, string>;
  logLines: string[];
  post(parts: Part[] | Buffer, options?: { contentType?: string; headers?: Record<string, string> }): Promise<{ status: number; headers: Record<string, unknown>; json: any; text: string }>;
  cleanup(): Promise<void>;
}

/** `tailnet`: the transport is the tailnet mode; every node is dialled on the port of the fake llama of `portOf` (default: nothing listens). */
async function lab(nodes: NodeSpec[], options: { tailnet?: { warnings: string[]; portOf?: string } } = {}): Promise<Lab> {
  const llamas: Record<string, FakeLlama> = {};
  const origins = new Map<string, string>();
  const nodeKeys = new Map<string, string>();
  const rows: SourceNodeRow[] = [];
  for (const [index, entry] of nodes.entries()) {
    const llama = await FakeLlama.start(entry.behaviour ?? okBehaviour);
    llamas[entry.id] = llama;
    origins.set(`${entry.id}.example.test`, llama.origin);
    nodeKeys.set(entry.id, `llama-${randomBytes(16).toString('hex')}`);
    const models = [];
    if (!entry.onlyText) models.push(model('stt-a', { inputModalities: entry.modalities ?? ['text', 'audio'] }));
    if (entry.alsoText || entry.onlyText) models.push(model('text-a'));
    rows.push(node(entry.id, models, { gatewayPriority: 10 + index, ...(entry.tailnet ?? {}) }));
  }
  const table = new RoutingTable({ load: async () => ({ rows, settings: { queueFactor: 4 } }) }, new FakeBus() as never, new FakeClock(), { timers: false, rebuildIntervalMs: 10_000 });
  table.onModuleInit();
  await table.rebuild();

  const ports = { a: 1, b: 1 };
  if (options.tailnet?.portOf) ports.a = ports.b = Number(new URL(llamas[options.tailnet.portOf]!.origin).port);
  const client = options.tailnet
    ? new UpstreamClient({
        version: 'test',
        targets: new TailnetTargets({
          suffix: { read: async () => null },
          ports,
          warn: (line) => options.tailnet!.warnings.push(line),
        }),
      })
    : new UpstreamClient({ version: 'test', resolveOrigin: (host) => origins.get(host) ?? 'http://127.0.0.1:1' });
  const settings: ProxySettings = { timeouts: { ...DEFAULT_GATEWAY_TIMEOUTS, connectMs: 500, firstByteMs: 3000, idleMs: 3000, nonStreamTotalMs: 8000 }, maxAttempts: 3 };
  const sink = new RecordingSink();
  const logLines: string[] = [];
  const loop = new AttemptLoop({ routing: table, keys: { get: async (id) => nodeKeys.get(id) ?? null }, transport: client, log: { warn: (line) => logLines.push(line) } });
  const service = new GatewayService(
    new FakeResolver(),
    { admit: async () => ({ headers: () => ({}), settle: () => undefined }) },
    sink,
    { handle: async () => { throw new GatewayError('LAI-GW-003'); } },
    { read: async () => settings },
    loop,
  );
  const gw = await buildGateway({ handlers: transcriptionsHandlers(service, audioCapabilityOf(table)) });

  return {
    gw,
    sink,
    llamas,
    nodeKeys,
    logLines,
    async post(parts, postOptions = {}) {
      const built = Buffer.isBuffer(parts) ? { payload: parts, contentType: postOptions.contentType ?? 'application/octet-stream' } : multipartBody(parts);
      const res = await gw.app.inject({
        method: 'POST',
        url: `${gw.prefix}/audio/transcriptions`,
        headers: { authorization: `Bearer ${gw.token}`, 'content-type': postOptions.contentType ?? built.contentType, ...(postOptions.headers ?? {}) },
        payload: built.payload,
      });
      let json: unknown = null;
      try {
        json = JSON.parse(res.body);
      } catch {
        json = null;
      }
      return { status: res.statusCode, headers: res.headers, json, text: res.body };
    },
    async cleanup() {
      await gw.app.close();
      await client.close();
      table.onModuleDestroy();
      await Promise.all(Object.values(llamas).map((llama) => llama.close()));
    },
  };
}

const labs: Lab[] = [];
afterEach(async () => {
  await Promise.all(labs.splice(0).map((entry) => entry.cleanup()));
});
const setup = async (...args: Parameters<typeof lab>): Promise<Lab> => {
  const created = await lab(...args);
  labs.push(created);
  return created;
};

const nestLogCalls: string[] = [];
beforeAll(() => {
  for (const level of ['log', 'debug', 'warn', 'error', 'verbose'] as const) {
    jest.spyOn(Logger.prototype, level).mockImplementation((...args: unknown[]) => {
      nestLogCalls.push(args.map((arg) => (typeof arg === 'string' ? arg : JSON.stringify(arg))).join(' '));
    });
  }
});
afterAll(() => jest.restoreAllMocks());
beforeEach(() => {
  nestLogCalls.length = 0;
});

const audioBytes = (size = 4096): Buffer => randomBytes(size);
const upload = (data: Buffer, extra: Part[] = [], model = 'lab-stt'): Part[] => [{ name: 'model', value: model }, ...extra, { name: 'file', file: { filename: 'speech.wav', data } }];

// ---- tests --------------------------------------------------------------------------------------------------------

describe('transcriptions: what the node receives', () => {
  it('forwards the file bytes unchanged (sha256 equal) with the model replaced by the resolved id, in a new multipart body', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const data = audioBytes(200_000);
    const client = multipartBody(
      [
        { name: 'model', value: 'lab-stt' },
        { name: 'language', value: 'es' },
        { name: 'prompt', value: 'una nota de voz' },
        { name: 'response_format', value: 'json' },
        { name: 'temperature', value: '0.2' },
        { name: 'timestamp_granularities[]', value: 'word' },
        { name: 'timestamp_granularities[]', value: 'segment' },
        { name: 'file', file: { filename: 'speech.wav', data, type: 'audio/wav' } },
      ],
      'client-boundary-0123',
    );
    const res = await l.post(client.payload, { contentType: client.contentType });
    expect(res.status).toBe(200);
    expect(res.json).toEqual({ text: 'hola mundo' });
    expect(res.headers['x-localai-node']).toBe('ai-n1');
    expect(res.headers['x-localai-attempts']).toBe('1');

    const seen = l.llamas['ai-n1']!.requests[0]!;
    expect(seen.method).toBe('POST');
    expect(seen.url).toBe('/v1/audio/transcriptions');
    const contentType = String(seen.headers['content-type']);
    expect(contentType).toMatch(/^multipart\/form-data; boundary=/);
    const parsed = parseMultipart(seen.rawBody, contentType);
    expect(parsed.boundary).not.toBe('client-boundary-0123'); // a new boundary
    expect(parsed.fields).toEqual([
      { name: 'model', value: 'stt-a' },
      { name: 'language', value: 'es' },
      { name: 'prompt', value: 'una nota de voz' },
      { name: 'response_format', value: 'json' },
      { name: 'temperature', value: '0.2' },
      { name: 'timestamp_granularities[]', value: 'word' },
      { name: 'timestamp_granularities[]', value: 'segment' },
    ]);
    expect(parsed.file).toMatchObject({ name: 'file', filename: 'speech.wav', type: 'audio/wav' });
    expect(sha256(parsed.file!.data)).toBe(sha256(data));
  });

  it('accepts the file before the model field', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const data = audioBytes();
    const res = await l.post([{ name: 'file', file: { filename: 'a.wav', data } }, { name: 'model', value: 'stt-a' }]);
    expect(res.status).toBe(200);
    const seen = l.llamas['ai-n1']!.requests[0]!;
    const parsed = parseMultipart(seen.rawBody, String(seen.headers['content-type']));
    expect(parsed.fields).toEqual([{ name: 'model', value: 'stt-a' }]);
    expect(sha256(parsed.file!.data)).toBe(sha256(data));
  });

  it('sends only the five gateway headers upstream (the node key, Content-Type, Accept, X-Request-ID, User-Agent)', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    await l.post(upload(audioBytes()), { headers: { cookie: 'session=abc', 'x-forwarded-for': '203.0.113.9', 'x-custom': 'nope' } });
    const headers = l.llamas['ai-n1']!.requests[0]!.headers;
    expect(headers.authorization).toBe(`Bearer ${l.nodeKeys.get('ai-n1')}`);
    expect(headers.authorization).not.toContain(l.gw.token);
    expect(headers.accept).toBe('application/json');
    expect(headers['user-agent']).toBe('localai-gateway/test');
    const allowed = new Set(['authorization', 'content-type', 'accept', 'x-request-id', 'user-agent', 'host', 'connection', 'content-length']);
    expect(Object.keys(headers).filter((name) => !allowed.has(name))).toEqual([]);
  });

  it('a file of exactly 25 MiB gets through, byte for byte', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const data = Buffer.alloc(25 * MIB, 0x5a);
    const res = await l.post(upload(data));
    expect(res.status).toBe(200);
    const seen = l.llamas['ai-n1']!.requests[0]!;
    expect(sha256(parseMultipart(seen.rawBody, String(seen.headers['content-type'])).file!.data)).toBe(sha256(data));
  });

  it('a retry on another node sends the same bytes again', async () => {
    const l = await setup([
      { id: 'ai-n1', behaviour: (_req, res) => respondJson(res, 503, { error: { message: 'busy' } }) },
      { id: 'ai-n2' },
    ]);
    const data = audioBytes(50_000);
    const res = await l.post(upload(data));
    expect(res.status).toBe(200);
    expect(res.headers['x-localai-node']).toBe('ai-n2');
    expect(res.headers['x-localai-attempts']).toBe('2');
    for (const id of ['ai-n1', 'ai-n2']) {
      const seen = l.llamas[id]!.requests[0]!;
      expect(sha256(parseMultipart(seen.rawBody, String(seen.headers['content-type'])).file!.data)).toBe(sha256(data));
    }
  });

  it('a 4xx from the node is the answer (LAI-GW-042), not a retry', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_req, res) => respondJson(res, 400, { error: { message: 'unsupported audio format' } }) }, { id: 'ai-n2' }]);
    const res = await l.post(upload(audioBytes()));
    expect(res.status).toBe(400);
    expect(res.json.error.localai_code).toBe('LAI-GW-042');
    expect(l.llamas['ai-n2']!.requests).toHaveLength(0);
  });

  it('a text response_format answer is forwarded with the node\'s content type', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_req, res) => { res.writeHead(200, { 'content-type': 'text/plain; charset=utf-8' }); res.end('hola mundo'); } }]);
    const res = await l.post(upload(audioBytes(), [{ name: 'response_format', value: 'text' }]));
    expect(res.status).toBe(200);
    expect(res.text).toBe('hola mundo');
    expect(res.headers['content-type']).toContain('text/plain');
  });
});

describe('transcriptions: limits and field rules', () => {
  it('25 MiB + 1 byte is 413 LAI-GW-004 and never reaches a node', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post(upload(Buffer.alloc(25 * MIB + 1, 1)));
    expect(res.status).toBe(413);
    expect(res.json.error).toMatchObject({ localai_code: 'LAI-GW-004', code: 'request_too_large', type: 'invalid_request_error' });
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });

  it('a request over the 26 MiB route limit is 413 LAI-GW-004 as well', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post(upload(Buffer.alloc(27 * MIB, 1)));
    expect(res.status).toBe(413);
    expect(res.json.error.localai_code).toBe('LAI-GW-004');
  });

  it('two files are 400 LAI-GW-003', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post([...upload(audioBytes()), { name: 'file', file: { filename: 'b.wav', data: audioBytes() } }]);
    expect(res.status).toBe(400);
    expect(res.json.error).toMatchObject({ localai_code: 'LAI-GW-003', code: 'unsupported_parameter', param: 'file' });
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });

  it('a file under another field name is 400 LAI-GW-003', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post([{ name: 'model', value: 'stt-a' }, { name: 'audio', file: { filename: 'a.wav', data: audioBytes() } }]);
    expect(res.status).toBe(400);
    expect(res.json.error).toMatchObject({ localai_code: 'LAI-GW-003', param: 'audio' });
  });

  it.each(['stream', 'unknown_field', 'file_url', 'timestamp_granularities'])('the field "%s" is 400 LAI-GW-003 (and named in param)', async (name) => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post(upload(audioBytes(), [{ name, value: 'true' }]));
    expect(res.status).toBe(400);
    expect(res.json.error).toMatchObject({ localai_code: 'LAI-GW-003', code: 'unsupported_parameter', param: name });
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });

  it('a field sent twice (other than timestamp_granularities[]) is 400 LAI-GW-003', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post(upload(audioBytes(), [{ name: 'language', value: 'es' }, { name: 'language', value: 'en' }]));
    expect(res.status).toBe(400);
    expect(res.json.error).toMatchObject({ localai_code: 'LAI-GW-003', param: 'language' });
  });

  it('more than 10 fields is 400 LAI-GW-003', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const many: Part[] = Array.from({ length: 11 }, () => ({ name: 'timestamp_granularities[]', value: 'word' }));
    const res = await l.post(upload(audioBytes(), many));
    expect(res.status).toBe(400);
    expect(res.json.error.localai_code).toBe('LAI-GW-003');
  });

  it('a missing model is 400 LAI-GW-002', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post([{ name: 'file', file: { filename: 'a.wav', data: audioBytes() } }]);
    expect(res.status).toBe(400);
    expect(res.json.error).toMatchObject({ localai_code: 'LAI-GW-002', code: 'missing_model', param: 'model' });
    const blank = await l.post(upload(audioBytes(), [], '  '));
    expect(blank.json.error.localai_code).toBe('LAI-GW-002');
  });

  it('a missing file and a body that is not multipart are 400 LAI-GW-001', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const noFile = await l.post([{ name: 'model', value: 'stt-a' }]);
    expect(noFile.status).toBe(400);
    expect(noFile.json.error.localai_code).toBe('LAI-GW-001');
    expect(noFile.json.error.message).toContain('"file"');
    const json = await l.post(Buffer.from(JSON.stringify({ model: 'stt-a' })), { contentType: 'application/json' });
    expect(json.status).toBe(400);
    expect(json.json.error.localai_code).toBe('LAI-GW-001');
    const garbage = await l.post(Buffer.from('not a multipart body'), { contentType: 'multipart/form-data; boundary=zz' });
    expect(garbage.status).toBe(400);
    expect(garbage.json.error.localai_code).toBe('LAI-GW-001');
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });

  it('needs a key like every other route', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const body = multipartBody(upload(audioBytes()));
    const res = await l.gw.app.inject({ method: 'POST', url: `${l.gw.prefix}/audio/transcriptions`, headers: { 'content-type': body.contentType }, payload: body.payload });
    expect(res.statusCode).toBe(401);
  });
});

describe('transcriptions: which models and nodes', () => {
  it('a model with no audio modality is 404 LAI-GW-020', async () => {
    const l = await setup([{ id: 'ai-n1', modalities: ['text'], alsoText: true }]);
    const res = await l.post(upload(audioBytes(), [], 'stt-a'));
    expect(res.status).toBe(404);
    expect(res.json.error).toMatchObject({ localai_code: 'LAI-GW-020', code: 'model_not_found', param: 'model' });
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
    expect(l.sink.records[0]).toMatchObject({ endpoint: 'audio.transcriptions', status: 404, errorCode: 'LAI-GW-020' });

    const textOnly = await l.post(upload(audioBytes(), [], 'text-a'));
    expect(textOnly.status).toBe(404);
    expect(textOnly.json.error.localai_code).toBe('LAI-GW-020');
  });

  it('an unknown model is 404 LAI-GW-020', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    expect((await l.post(upload(audioBytes(), [], 'nope'))).json.error.localai_code).toBe('LAI-GW-020');
  });

  it('goes only to nodes whose model reports audio, and a skipped node is not an attempt', async () => {
    const l = await setup([
      { id: 'ai-n1', modalities: ['text'] },
      { id: 'ai-n2', modalities: ['text', 'audio'] },
    ]);
    const res = await l.post(upload(audioBytes()));
    expect(res.status).toBe(200);
    expect(res.headers['x-localai-node']).toBe('ai-n2');
    expect(res.headers['x-localai-attempts']).toBe('1');
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });
});

describe('transcriptions: usage and privacy', () => {
  it('records endpoint audio.transcriptions, usageSource none and 0 tokens', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    await l.post(upload(audioBytes()));
    expect(l.sink.records).toHaveLength(1);
    expect(l.sink.records[0]).toMatchObject({
      endpoint: 'audio.transcriptions',
      model: 'stt-a',
      requestedModel: 'lab-stt',
      nodeId: 'ai-n1',
      status: 200,
      errorCode: null,
      stream: false,
      aborted: false,
      attempts: 1,
      promptTokens: 0,
      completionTokens: 0,
      usageSource: 'none',
    });
  });

  it('nothing of the audio or the transcript reaches a log line, a usage row or a header', async () => {
    const marker = `AUDIO-MARKER-${randomBytes(12).toString('hex')}`;
    const l = await setup([{ id: 'ai-n1', behaviour: (_req, res) => respondJson(res, 200, { text: `heard ${marker}` }) }, { id: 'ai-n2' }]);
    const data = Buffer.concat([randomBytes(512), Buffer.from(marker), randomBytes(512)]);
    // One success and one failure path, so both kinds of log line and usage row exist.
    const ok = await l.post(upload(data, [{ name: 'prompt', value: `${marker}-prompt` }]));
    expect(ok.status).toBe(200);
    await l.post(upload(data, [{ name: 'nope', value: marker }]));
    const everything = JSON.stringify({ logs: l.gw.logs, nest: nestLogCalls, attempts: l.logLines, usage: l.sink.records, headers: ok.headers });
    expect(everything).not.toContain(marker);
    expect(l.gw.logs.info.length).toBeGreaterThan(0);
    for (const line of l.gw.logs.info) expect(JSON.parse(line)).toMatchObject({ endpoint: 'audio.transcriptions' });
  });
});

describe('transcriptions in tailnet mode (Pattern A, plain HTTP)', () => {
  /** An address built at run time, in a range nothing answers on: it only has to never show up in any output. */
  const markerIp = (): string => ['100', String(64 + randomInt(0, 60)), String(randomInt(1, 250)), String(randomInt(1, 250))].join('.');

  it('dials http://<tailnetIp>:<port> and leaves out a node with no address, once, naming only the node id', async () => {
    const warnings: string[] = [];
    // ai-n1 comes first by priority but has no tailnet address; ai-n2 has one (loopback, dialled on the fake llama's port).
    const l = await setup(
      [
        { id: 'ai-n1', tailnet: { servePattern: 'A', tailnetIp: null, tailnetMachine: null } },
        { id: 'ai-n2', tailnet: { servePattern: 'A', tailnetIp: '127.0.0.1', tailnetMachine: null } },
      ],
      { tailnet: { warnings, portOf: 'ai-n2' } },
    );

    const data = audioBytes(20_000);
    const first = await l.post(upload(data));
    const second = await l.post(upload(data));
    expect(first.status).toBe(200);
    expect(second.status).toBe(200);
    expect(first.headers['x-localai-node']).toBe('ai-n2');
    expect(first.headers['x-localai-attempts']).toBe('1');
    expect(l.llamas['ai-n2']!.requests).toHaveLength(2);
    expect(l.llamas['ai-n2']!.requests[0]!.headers.host).toBe(`127.0.0.1:${new URL(l.llamas['ai-n2']!.origin).port}`);
    expect(warnings).toEqual(['node ai-n1 is left out in tailnet mode: no_tailnet_ip']);
    expect(l.sink.records.map((record) => record.nodeId)).toEqual(['ai-n2', 'ai-n2']);
  });

  it('an unreachable tailnet address is LAI-GW-040 and the address is in no log line, error or header', async () => {
    const ip = markerIp();
    const warnings: string[] = [];
    const l = await setup([{ id: 'ai-n1', tailnet: { servePattern: 'A', tailnetIp: ip, tailnetMachine: 'machine-x' } }], { tailnet: { warnings } });
    const res = await l.post(upload(audioBytes()));
    expect(res.status).toBe(502);
    expect(res.json.error.localai_code).toBe('LAI-GW-040');
    const everything = JSON.stringify({ logs: l.gw.logs, nest: nestLogCalls, attempts: l.logLines, warnings, usage: l.sink.records, headers: res.headers, body: res.json });
    expect(everything).not.toContain(ip);
    expect(res.headers['x-localai-node']).toBe('ai-n1');
  });
});
