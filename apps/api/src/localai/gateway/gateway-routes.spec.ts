// The executable form of spec section 3.1 point 7: EAB's "every Nest route has @Auth()" check cannot see raw routes, so
// this suite enumerates every route registered under the gateway prefix and proves each one refuses an anonymous call,
// except `GET /v1/` (the index) and `OPTIONS` (CORS preflight, which a browser sends without credentials). It also proves
// the order: the authenticator runs BEFORE any handler.

import type { GatewayPrincipal } from './auth';
import { buildGateway, type TestGateway } from './gateway.testkit';
import type { GatewayHandler } from './routes';

/** The only routes that may answer without a key. */
const PUBLIC = new Set(['GET /api/gateway/v1/', 'HEAD /api/gateway/v1/', 'GET /api/gateway/v1', 'HEAD /api/gateway/v1', 'OPTIONS /api/gateway/v1/*']);

const concreteUrl = (url: string) => url.replace(/:[A-Za-z]+/g, 'some-id').replace('*', 'anything/else');

/** Every route that answers a keyless request with anything but 401 LAI-GW-010 (and is not public). */
async function unauthenticatedRoutes(gateway: TestGateway): Promise<string[]> {
  const offenders: string[] = [];
  for (const route of gateway.routes) {
    const id = `${route.method} ${route.url}`;
    if (PUBLIC.has(id)) continue;
    const response = await gateway.app.inject({ method: route.method as 'GET', url: concreteUrl(route.url) });
    // A HEAD answer has no body to read; its status is the proof.
    const code = response.statusCode !== 401 ? undefined : route.method === 'HEAD' ? 'LAI-GW-010' : (response.json() as { error?: { localai_code?: string } }).error?.localai_code;
    if (code !== 'LAI-GW-010') offenders.push(`${id} -> ${response.statusCode}`);
  }
  return offenders;
}

describe('gateway routes: authentication on every route', () => {
  let gateway: TestGateway | undefined;
  afterEach(async () => {
    await gateway?.app.close();
    gateway = undefined;
  });

  it('registers the spec endpoints and the catch-alls (and nothing else)', async () => {
    gateway = await buildGateway();
    const ids = gateway.routes.map((r) => `${r.method} ${r.url}`).filter((id) => !id.startsWith('HEAD '));
    expect(ids.sort()).toEqual(
      [
        'GET /api/gateway/v1', // also answers with the trailing slash (Fastify's prefix rule)
        'GET /api/gateway/v1/models',
        'GET /api/gateway/v1/models/:id',
        'POST /api/gateway/v1/chat/completions',
        'POST /api/gateway/v1/completions',
        'POST /api/gateway/v1/embeddings',
        'POST /api/gateway/v1/audio/transcriptions',
        'OPTIONS /api/gateway/v1/*',
        'GET /api/gateway/v1/*',
        'POST /api/gateway/v1/*',
        'PUT /api/gateway/v1/*',
        'PATCH /api/gateway/v1/*',
        'DELETE /api/gateway/v1/*',
      ].sort(),
    );
  });

  it('every route except the index and OPTIONS refuses a keyless request with 401 LAI-GW-010', async () => {
    gateway = await buildGateway();
    expect(await unauthenticatedRoutes(gateway)).toEqual([]);
  });

  it('FAILS when a route is added that answers without authenticating (the guard is real)', async () => {
    gateway = await buildGateway({
      beforeReady: (app) => {
        app.get('/api/gateway/v1/rogue', async () => ({ leaked: true }));
      },
    });
    expect(await unauthenticatedRoutes(gateway)).toEqual(['GET /api/gateway/v1/rogue -> 200', 'HEAD /api/gateway/v1/rogue -> 200']);
  });

  it('authenticate() runs before the handler of every authenticated route, and the index and OPTIONS never call it', async () => {
    const calls: string[] = [];
    const principal = { key: { id: 'k', tokenPrefix: 'lai_abcdef' }, userId: 'u' } as unknown as GatewayPrincipal;
    const seen = (name: string): GatewayHandler => async ({ reply }) => {
      calls.push(`handler:${name}`);
      return reply.code(200).send({ ok: true });
    };
    gateway = await buildGateway({
      authenticator: {
        authenticate: async () => {
          calls.push('authenticate');
          return principal;
        },
      },
      handlers: { listModels: seen('models'), getModel: seen('model'), chatCompletions: seen('chat'), completions: seen('completions'), embeddings: seen('embeddings'), transcriptions: seen('audio') },
    });
    const send = (method: 'GET' | 'POST' | 'OPTIONS', url: string, payload?: object) => (gateway as TestGateway).app.inject({ method, url: `/api/gateway${url}`, payload });

    await send('GET', '/v1/models');
    await send('GET', '/v1/models/m-1');
    await send('POST', '/v1/chat/completions', { model: 'm' });
    await send('POST', '/v1/completions', { model: 'm' });
    await send('POST', '/v1/embeddings', { model: 'm' });
    await send('POST', '/v1/audio/transcriptions', {});
    expect(calls).toEqual([
      'authenticate', 'handler:models',
      'authenticate', 'handler:model',
      'authenticate', 'handler:chat',
      'authenticate', 'handler:completions',
      'authenticate', 'handler:embeddings',
      'authenticate', 'handler:audio',
    ]);

    calls.length = 0;
    expect((await send('GET', '/v1/')).statusCode).toBe(200);
    expect((await send('OPTIONS', '/v1/chat/completions')).statusCode).toBe(204);
    expect(calls).toEqual([]);
  });

  it('an unknown route authenticates first too: anonymous 401, then 404 endpoint_not_supported with a key', async () => {
    gateway = await buildGateway();
    const anonymous = await gateway.app.inject({ method: 'GET', url: '/api/gateway/v1/audio/speech' });
    expect(anonymous.statusCode).toBe(401);
    const keyed = await gateway.app.inject({ method: 'POST', url: '/api/gateway/v1/audio/speech', headers: { authorization: `Bearer ${gateway.token}` }, payload: { model: 'x' } });
    expect(keyed.statusCode).toBe(404);
    expect(keyed.json()).toMatchObject({ error: { code: 'endpoint_not_supported', localai_code: 'LAI-GW-024', type: 'not_found_error' } });
  });
});

