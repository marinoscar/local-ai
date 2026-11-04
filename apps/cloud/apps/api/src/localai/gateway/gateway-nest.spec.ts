// The gateway scope on a real Nest + Fastify application, wired the way `createApp()` wires it: global prefix `api`,
// the services resolved from the Nest container by `registerGatewayRoutes(app)`. Proves a raw route coexists with Nest's
// own routes (which keep the EAB `{ data }` envelope) and is not touched by it.
import { Controller, Get, Module } from '@nestjs/common';
import { MaintenanceModeService } from '@marinoscar/platform-api/host';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { Test } from '@nestjs/testing';

import { GatewayAuthenticator, type GatewayPrincipal } from './auth';
import { GatewayError } from './errors';
import { GatewayService } from './gateway.service';
import { GatewaySettingsReader } from './gateway-settings';
import { registerGatewayRoutes } from './routes';

@Controller('ping')
class PingController {
  @Get()
  ping() {
    return { pong: true };
  }
}

@Module({ controllers: [PingController] })
class PingModule {}

describe('registerGatewayRoutes on a Nest application', () => {
  let app: NestFastifyApplication;
  let maintenance = false;
  const calls: string[] = [];

  beforeAll(async () => {
    const principal = { key: { tokenPrefix: 'lai_abcdef' }, userId: 'user-a' } as unknown as GatewayPrincipal;
    const moduleRef = await Test.createTestingModule({
      imports: [PingModule],
      providers: [
        { provide: MaintenanceModeService, useValue: { isEnabled: async () => maintenance } },
        {
          provide: GatewayAuthenticator,
          useValue: {
            authenticate: async (input: { authorization?: string }) => {
              calls.push('authenticate');
              if (input.authorization !== 'Bearer ok') {
                throw new GatewayError('LAI-GW-010');
              }
              return principal;
            },
          },
        },
        { provide: GatewaySettingsReader, useValue: { enabled: () => true, corsOrigins: async () => [] } },
        { provide: GatewayService, useValue: { handle: async () => undefined } },
      ],
    }).compile();
    app = moduleRef.createNestApplication<NestFastifyApplication>(new FastifyAdapter());
    app.setGlobalPrefix('api');
    await registerGatewayRoutes(app);
    await app.init();
    await app.getHttpAdapter().getInstance().ready();
  });

  afterAll(async () => {
    await app.close();
  });

  it('serves the index raw (no { data } envelope) next to Nest routes', async () => {
    const index = await app.inject({ method: 'GET', url: '/api/gateway/v1/' });
    expect(index.statusCode).toBe(200);
    expect(index.json()).toMatchObject({ object: 'localai.gateway' });
    expect(index.json()).not.toHaveProperty('data');
    expect(index.headers['x-request-id']).toBeDefined();
  });

  it('leaves Nest routes alone', async () => {
    const ping = await app.inject({ method: 'GET', url: '/api/ping' });
    expect(ping.statusCode).toBe(200);
    expect(ping.json()).toEqual({ pong: true });
  });

  it('answers errors in the OpenAI shape, not the EAB one, and calls the authenticator', async () => {
    const response = await app.inject({ method: 'GET', url: '/api/gateway/v1/models' });
    expect(response.statusCode).toBe(401);
    expect(response.json()).toEqual({
      error: expect.objectContaining({ code: 'invalid_api_key', localai_code: 'LAI-GW-010', type: 'authentication_error', param: null }),
    });
    expect(calls).toContain('authenticate');
  });

  it('a keyed request reaches the stub (503 gateway_unavailable), and an unknown path is the 404 endpoint_not_supported', async () => {
    const headers = { authorization: 'Bearer ok' };
    const stub = await app.inject({ method: 'GET', url: '/api/gateway/v1/models', headers });
    expect(stub.statusCode).toBe(503);
    const unknown = await app.inject({ method: 'GET', url: '/api/gateway/v1/nope', headers });
    expect(unknown.statusCode).toBe(404);
    expect(unknown.json().error.localai_code).toBe('LAI-GW-024');
  });

  it('asks MaintenanceModeService per request', async () => {
    maintenance = true;
    const response = await app.inject({ method: 'GET', url: '/api/gateway/v1/' });
    maintenance = false;
    expect(response.statusCode).toBe(503);
    expect(response.json().error.localai_code).toBe('LAI-GW-051');
    expect((await app.inject({ method: 'GET', url: '/api/gateway/v1/' })).statusCode).toBe(200);
  });
});
