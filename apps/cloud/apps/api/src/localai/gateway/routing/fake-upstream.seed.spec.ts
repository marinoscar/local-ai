// The fake-upstream load-test flag (GW-12): it must be impossible to turn on in production, it must stay on loopback, and
// when on it must give the gateway exactly one routable synthetic node, its key, its catalog and its transport.
import { createServer, type IncomingHttpHeaders, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';

import { Logger } from '@nestjs/common';

import { parseModelsYaml } from '../models/catalog-hints';
import { UpstreamClient } from '../upstream/client';
import { createUpstreamTransport } from '../upstream/transport.factory';
import { buildSnapshot } from './build-candidates';
import { noopEndpointAlerts } from './endpoint-alerts.port';
import {
  assertFakeUpstreamAllowed,
  catalogStoreFor,
  endpointAlertsFor,
  FAKE_MODEL_ID,
  FAKE_NODE_ID,
  FAKE_UPSTREAM_FLAG,
  FAKE_UPSTREAM_URL_VAR,
  FakeCatalogStore,
  FakeHealthClient,
  FakeNodeKeyProvider,
  FakeRoutingSource,
  FakeUpstreamRefusedError,
  fakeUpstreamFromEnv,
  fakeUpstreamRequested,
  healthClientFor,
  nodeKeysFor,
  parseFakeUpstreamOrigin,
  routingSourceFor,
} from './fake-upstream.seed';
import { NodeKeyProvider } from './node-key.provider';
import type { HealthClient } from './probe';
import type { RoutingSource } from './routing-source';

const env = (values: Record<string, string>): NodeJS.ProcessEnv => values as NodeJS.ProcessEnv;
const ON = { [FAKE_UPSTREAM_FLAG]: '1' };
const PRODUCTION = { NODE_ENV: 'production' };

describe('the fake-upstream flag', () => {
  beforeAll(() => {
    jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
  });
  afterAll(() => jest.restoreAllMocks());

  describe('what counts as on', () => {
    it.each([['1'], ['true'], ['TRUE'], ['yes'], [' on ']])('%j is on', (value) => {
      expect(fakeUpstreamRequested(env({ [FAKE_UPSTREAM_FLAG]: value }))).toBe(true);
    });
    it.each([[''], ['  '], ['0'], ['false'], ['FALSE']])('%j is off', (value) => {
      expect(fakeUpstreamRequested(env({ [FAKE_UPSTREAM_FLAG]: value }))).toBe(false);
    });
    it('is off when unset', () => {
      expect(fakeUpstreamRequested(env({}))).toBe(false);
      expect(fakeUpstreamFromEnv(env({}))).toBeNull();
    });
  });

  describe('production refusal', () => {
    it.each([['1'], ['true'], ['yes'], ['anything']])('refuses %j with NODE_ENV=production', (value) => {
      const e = env({ ...PRODUCTION, [FAKE_UPSTREAM_FLAG]: value });
      expect(() => assertFakeUpstreamAllowed(e)).toThrow(FakeUpstreamRefusedError);
      expect(() => fakeUpstreamFromEnv(e)).toThrow(FakeUpstreamRefusedError);
    });

    it('refuses whatever the spelling of NODE_ENV', () => {
      for (const nodeEnv of ['Production', ' production ', 'PRODUCTION']) {
        expect(() => assertFakeUpstreamAllowed(env({ NODE_ENV: nodeEnv, ...ON }))).toThrow(FakeUpstreamRefusedError);
      }
    });

    it('names the variable in the message, so the operator knows what to unset', () => {
      expect(() => assertFakeUpstreamAllowed(env({ ...PRODUCTION, ...ON }))).toThrow(FAKE_UPSTREAM_FLAG);
    });

    it('allows an explicit off in production, and the flag outside production', () => {
      expect(() => assertFakeUpstreamAllowed(env({ ...PRODUCTION, [FAKE_UPSTREAM_FLAG]: '0' }))).not.toThrow();
      expect(() => assertFakeUpstreamAllowed(env(PRODUCTION))).not.toThrow();
      for (const nodeEnv of ['development', 'test', '']) expect(() => assertFakeUpstreamAllowed(env({ NODE_ENV: nodeEnv, ...ON }))).not.toThrow();
      expect(() => assertFakeUpstreamAllowed(env(ON))).not.toThrow();
    });

    it('refuses in EVERY seam, so a path that skips main.ts still cannot start with the flag in production', () => {
      const e = env({ ...PRODUCTION, ...ON });
      const realSource = { load: jest.fn() } as RoutingSource;
      const realKeys = new NodeKeyProvider({} as never);
      const realHealth = { check: jest.fn() } as HealthClient;
      const realStore = { head: jest.fn(), modelsYaml: jest.fn() };
      expect(() => routingSourceFor(realSource, e)).toThrow(FakeUpstreamRefusedError);
      expect(() => nodeKeysFor(realKeys, e)).toThrow(FakeUpstreamRefusedError);
      expect(() => healthClientFor(realHealth, e)).toThrow(FakeUpstreamRefusedError);
      expect(() => endpointAlertsFor(noopEndpointAlerts, e)).toThrow(FakeUpstreamRefusedError);
      expect(() => catalogStoreFor(realStore, e)).toThrow(FakeUpstreamRefusedError);
      expect(() => createUpstreamTransport({ version: '0.0.0', env: e, tailnetSuffix: { read: async () => null } })).toThrow(FakeUpstreamRefusedError);
    });
  });

  describe('the URL', () => {
    it('defaults to the loopback echo server', () => {
      expect(fakeUpstreamFromEnv(env(ON))).toEqual({ origin: 'http://127.0.0.1:18999' });
    });

    it.each([
      ['http://127.0.0.1:19000', 'http://127.0.0.1:19000'],
      ['http://127.0.0.1:19000/', 'http://127.0.0.1:19000'],
      ['http://localhost:19000', 'http://localhost:19000'],
      ['http://[::1]:19000', 'http://[::1]:19000'],
      ['http://127.5.4.3:80', 'http://127.5.4.3'],
    ])('accepts %s', (raw, origin) => {
      expect(parseFakeUpstreamOrigin(raw)).toBe(origin);
    });

    it.each([
      ['https://127.0.0.1:19000'],
      ['http://example.test:19000'],
      ['http://other-host.example.test:19000'],
      // A private-range address, built at run time so no address literal is committed.
      [`http://${[10, 0, 0, 5].join('.')}:19000`],
      ['http://0.0.0.0:19000'],
      ['http://127.0.0.1:19000/v1'],
      ['http://user:pw@127.0.0.1:19000'],
      ['http://127.0.0.1:19000/?x=1'],
      ['not a url'],
      ['file:///etc/passwd'],
    ])('refuses %s', (raw) => {
      expect(parseFakeUpstreamOrigin(raw)).toBeNull();
      expect(() => fakeUpstreamFromEnv(env({ ...ON, [FAKE_UPSTREAM_URL_VAR]: raw }))).toThrow(FakeUpstreamRefusedError);
    });
  });

  describe('the synthetic node', () => {
    it('is routable and serves the fake model, at any time of day (its timestamps follow `now`)', async () => {
      for (const now of [new Date('2026-03-01T12:00:00Z'), new Date('2030-01-01T00:00:00Z')]) {
        const { rows, settings } = await new FakeRoutingSource().load(now);
        expect(rows).toHaveLength(1);
        expect(settings.queueFactor).toBeGreaterThanOrEqual(1);
        const snapshot = buildSnapshot(rows, now);
        expect([...snapshot.routable.keys()]).toEqual([FAKE_NODE_ID]);
        const candidates = snapshot.candidates.get(FAKE_MODEL_ID) ?? [];
        expect(candidates.map((c) => [c.nodeId, c.status])).toEqual([[FAKE_NODE_ID, 'loaded']]);
        // 200 streams must fit under the saturation point (queueFactor x slots).
        expect(candidates[0].slots * settings.queueFactor).toBeGreaterThan(200);
      }
    });

    it('uses an invented host that cannot resolve to a real machine', async () => {
      const { rows } = await new FakeRoutingSource().load(new Date());
      expect(rows[0].publicHost.endsWith('.example.test')).toBe(true);
      expect(rows[0].tailnetIp).toBeNull();
    });

    it('has a random key for itself and none for any other node', async () => {
      const a = new FakeNodeKeyProvider();
      const b = new FakeNodeKeyProvider();
      const keyA = await a.get(FAKE_NODE_ID);
      expect(typeof keyA).toBe('string');
      expect(keyA).toBe(await a.get(FAKE_NODE_ID));
      expect(keyA).not.toBe(await b.get(FAKE_NODE_ID));
      expect(await a.get('some-real-node')).toBeNull();
    });

    it('is an approved catalog of exactly one model with the chat role', async () => {
      const store = new FakeCatalogStore();
      const head = await store.head();
      expect(head).not.toBeNull();
      const yaml = await store.modelsYaml(head!.version);
      expect(yaml).not.toBeNull();
      const models = parseModelsYaml(yaml!);
      expect([...models.keys()]).toEqual([FAKE_MODEL_ID]);
      expect(models.get(FAKE_MODEL_ID)?.roles).toEqual(['chat']);
      expect(await store.modelsYaml('another-version')).toBeNull();
    });

    it('is probed at the echo server /health, not at its public host', async () => {
      const inner = { check: jest.fn().mockResolvedValue({ ok: true }) } satisfies HealthClient;
      const client = new FakeHealthClient('http://127.0.0.1:18999', inner);
      await expect(client.check('https://fake-load-node.example.test/health', 3000)).resolves.toEqual({ ok: true });
      expect(inner.check).toHaveBeenCalledWith('http://127.0.0.1:18999/health', 3000);
    });
  });

  describe('the selectors', () => {
    it('hand back the real provider when the flag is off', () => {
      const realSource = { load: jest.fn() } as RoutingSource;
      const realKeys = new NodeKeyProvider({} as never);
      const realHealth = { check: jest.fn() } as HealthClient;
      const realStore = { head: jest.fn(), modelsYaml: jest.fn() };
      expect(routingSourceFor(realSource, env({}))).toBe(realSource);
      expect(nodeKeysFor(realKeys, env({}))).toBe(realKeys);
      expect(healthClientFor(realHealth, env({}))).toBe(realHealth);
      expect(endpointAlertsFor(noopEndpointAlerts, env({}))).toBe(noopEndpointAlerts);
      expect(catalogStoreFor(realStore, env({}))).toBe(realStore);
    });

    it('hand back the fakes when the flag is on', () => {
      const realSource = { load: jest.fn() } as RoutingSource;
      expect(routingSourceFor(realSource, env(ON))).toBeInstanceOf(FakeRoutingSource);
      expect(nodeKeysFor(new NodeKeyProvider({} as never), env(ON))).toBeInstanceOf(FakeNodeKeyProvider);
      expect(healthClientFor({ check: jest.fn() }, env(ON))).toBeInstanceOf(FakeHealthClient);
      expect(catalogStoreFor({ head: jest.fn(), modelsYaml: jest.fn() }, env(ON))).toBeInstanceOf(FakeCatalogStore);
    });
  });

  describe('the transport', () => {
    let server: Server;
    let seen: Array<{ url: string | undefined; headers: IncomingHttpHeaders }>;
    let origin: string;

    beforeAll(async () => {
      seen = [];
      server = createServer((request, response) => {
        seen.push({ url: request.url, headers: request.headers });
        request.resume();
        response.writeHead(200, { 'content-type': 'application/json' });
        response.end('{}');
      });
      await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
      origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    });
    afterAll(async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    });

    it('sends every call to the echo server, whatever the node host, and is the normal client otherwise', async () => {
      const transport = createUpstreamTransport({ version: '0.0.0', env: env({ ...ON, [FAKE_UPSTREAM_URL_VAR]: origin }), tailnetSuffix: { read: async () => null } });
      expect(transport).toBeInstanceOf(UpstreamClient);
      const response = await transport.call({
        nodeId: FAKE_NODE_ID,
        publicHost: 'fake-load-node.example.test',
        path: '/v1/chat/completions',
        body: Buffer.from('{}'),
        contentType: 'application/json',
        accept: 'text/event-stream',
        requestId: 'req-1',
        apiKey: 'node-key-placeholder',
        signal: new AbortController().signal,
        connectMs: 1000,
        firstByteMs: 5000,
        idleMs: 5000,
      });
      expect(response.status).toBe(200);
      response.destroy();
      expect(seen).toHaveLength(1);
      expect(seen[0].url).toBe('/v1/chat/completions');
      expect(seen[0].headers.authorization).toBe('Bearer node-key-placeholder');
      await (transport as UpstreamClient).close();
    });

    it('is the public-mode client when the flag is off', () => {
      const transport = createUpstreamTransport({ version: '0.0.0', env: env({}), tailnetSuffix: { read: async () => null } });
      expect(transport).toBeInstanceOf(UpstreamClient);
    });
  });
});
