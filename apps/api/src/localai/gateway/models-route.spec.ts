// `GET /v1/models` and `GET /v1/models/{id}` through the real gateway pipeline (Fastify inject over the fakes of the
// gateway testkit, a real routing table and a real listing over the fixture lab).
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { Logger } from '@nestjs/common';

import { buildGateway, makeKey, makeToken, type TestGateway } from './gateway.testkit';
import { ModelListing } from './models/listing';
import { FakeCatalogStore, FakePolicySource, labNodes, labPolicies, readerOf, tableOf } from './models/models.fixtures';
import { ModelDirectoryProvider } from './models/resolve';
import { modelsHandlers } from './models-route';

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

const FIXTURE = join(__dirname, '../../../test/fixtures/localai/gateway-models.json');

let gateway: TestGateway;
let catalogStore: FakeCatalogStore;

async function start(): Promise<void> {
  catalogStore = new FakeCatalogStore();
  const { table, clock } = await tableOf(labNodes());
  const directory = new ModelDirectoryProvider(readerOf(catalogStore, clock), new FakePolicySource(labPolicies()), clock);
  gateway = await buildGateway({ handlers: modelsHandlers(new ModelListing(directory, table)) });
}

beforeEach(start);
afterEach(async () => gateway.app.close());

const get = (path: string, token = gateway.token) =>
  gateway.app.inject({ method: 'GET', url: `${gateway.prefix}${path}`, headers: { authorization: `Bearer ${token}` } });

/** A second key with the given scope, owned by the active test owner. */
function scopedToken(allowedModels: string[]): string {
  const token = makeToken();
  gateway.store.add(makeKey(token, { allowedModels }));
  return token;
}

describe('GET /v1/models', () => {
  it('answers the fixture of the lab, as JSON', async () => {
    const response = await get('/models');
    expect(response.statusCode).toBe(200);
    expect(response.headers['content-type']).toMatch(/^application\/json/);
    expect(response.headers['x-request-id']).toBeTruthy();
    expect(response.json()).toEqual(JSON.parse(readFileSync(FIXTURE, 'utf8')));
  });

  it('needs a key', async () => {
    const response = await gateway.app.inject({ method: 'GET', url: `${gateway.prefix}/models` });
    expect(response.statusCode).toBe(401);
    expect(response.json().error.localai_code).toBe('LAI-GW-010');
  });

  it('shows a key only what its scope allows', async () => {
    const response = await get('/models', scopedToken(['qwen3.5-9b-q4km']));
    expect((response.json().data as Array<{ id: string }>).map((entry) => entry.id)).toEqual(['qwen3.5-9b-q4km']);
  });

  it('an alias-only scope lists the alias and not the underlying id', async () => {
    const response = await get('/models', scopedToken(['lab-coder']));
    expect((response.json().data as Array<{ id: string }>).map((entry) => entry.id)).toEqual(['lab-coder']);
  });

  it('is an empty list, not an error, when no catalog is approved', async () => {
    await gateway.app.close();
    catalogStore = new FakeCatalogStore(null);
    const { table, clock } = await tableOf(labNodes());
    const directory = new ModelDirectoryProvider(readerOf(catalogStore, clock), new FakePolicySource(labPolicies()), clock);
    gateway = await buildGateway({ handlers: modelsHandlers(new ModelListing(directory, table)) });
    const response = await get('/models');
    expect(response.statusCode).toBe(200);
    expect(response.json()).toEqual({ object: 'list', data: [] });
  });
});

describe('GET /v1/models/{id}', () => {
  it('answers the single entry of an id', async () => {
    const response = await get('/models/qwen3.5-9b-q4km');
    expect(response.statusCode).toBe(200);
    const fixture = JSON.parse(readFileSync(FIXTURE, 'utf8')).data.find((entry: { id: string }) => entry.id === 'qwen3.5-9b-q4km');
    expect(response.json()).toEqual(fixture);
  });

  it('answers the entry of an alias, and of a loadable model', async () => {
    expect((await get('/models/lab-coder')).json()['x-localai']).toMatchObject({ alias_of: 'qwen3-coder-next-q4km', state: 'loaded' });
    expect((await get('/models/qwen3.6-35b-a3b-q4km')).json()['x-localai']).toMatchObject({ state: 'loadable', load_wait_s: 120 });
  });

  it('is 404 model_not_found (LAI-GW-020) for an id that is not listed', async () => {
    for (const id of ['unknown-model', 'qwen3.5-4b-q4km', 'catalog-only-model', 'whisper-small']) {
      const response = await get(`/models/${id}`);
      expect(response.statusCode).toBe(404);
      expect(response.json().error).toMatchObject({ type: 'not_found_error', code: 'model_not_found', localai_code: 'LAI-GW-020' });
    }
  });

  it('is 404 for an alias the key is not scoped to (and for the id behind a scoped alias)', async () => {
    const token = scopedToken(['qwen3.5-9b-q4km']);
    expect((await get('/models/qwen3.5-9b-q4km', token)).statusCode).toBe(200);
    expect((await get('/models/lab-coder', token)).statusCode).toBe(404);
    const aliasOnly = scopedToken(['lab-coder']);
    expect((await get('/models/lab-coder', aliasOnly)).statusCode).toBe(200);
    expect((await get('/models/qwen3-coder-next-q4km', aliasOnly)).statusCode).toBe(404);
  });

  it('needs a key', async () => {
    const response = await gateway.app.inject({ method: 'GET', url: `${gateway.prefix}/models/qwen3.5-9b-q4km` });
    expect(response.statusCode).toBe(401);
  });

  it('records the requested id in the request log without the body', async () => {
    await get('/models/qwen3.5-9b-q4km');
    expect(gateway.logs.info.some((line) => line.includes('"requested_model":"qwen3.5-9b-q4km"') && line.includes('"endpoint":"models"'))).toBe(true);
  });
});
