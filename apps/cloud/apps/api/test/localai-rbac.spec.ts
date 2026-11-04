// The Local AI permissions over HTTP: the real guards and the real seeded
// grants, on the whole AppModule over a mocked database (no PostgreSQL).
//
// A test-only probe controller stands in for the real Local AI routes
// (CLOUD-09, CLOUD-10), one route per permission, so the RBAC wiring is proven
// before those routes exist. The probe lives in this file, never in `src/`:
// production exposes no route for it.
import fastifyCookie from '@fastify/cookie';
import { Controller, Get } from '@nestjs/common';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { Auth } from '@marinoscar/platform-api/identity';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../src/platform/registrations';
import { AppModule } from '../src/app.module';
import { LOCALAI_PERMISSIONS } from '../src/localai/localai.permissions';
import { PrismaService } from '../src/prisma/prisma.service';
import { PrismaSystemService } from '../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from './ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from './ai-conformance/prisma.mock';

@Controller('localai-probe')
class LocalAiProbeController {
  @Get('read')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  read() {
    return { ok: true };
  }

  @Get('operate')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_OPERATE.id] })
  operate() {
    return { ok: true };
  }

  @Get('lab-admin')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  labAdmin() {
    return { ok: true };
  }

  @Get('gateway-use')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.GATEWAY_USE.id] })
  gatewayUse() {
    return { ok: true };
  }

  @Get('gateway-admin')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.GATEWAY_ADMIN.id] })
  gatewayAdmin() {
    return { ok: true };
  }
}

const ROUTES: ReadonlyArray<{ path: string; permission: string }> = [
  { path: 'read', permission: 'lab:read' },
  { path: 'operate', permission: 'lab:operate' },
  { path: 'lab-admin', permission: 'lab:admin' },
  { path: 'gateway-use', permission: 'gateway:use' },
  { path: 'gateway-admin', permission: 'gateway:admin' },
];

/** Who may call which probe route: the same table as spec §3.4. */
const ALLOWED: Readonly<Record<FixtureRole, readonly string[]>> = {
  admin: ['lab:read', 'lab:operate', 'lab:admin', 'gateway:use', 'gateway:admin'],
  contributor: ['lab:read', 'lab:operate', 'gateway:use'],
  viewer: ['lab:read'],
};

describe('Local AI permissions over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({ imports: [AppModule], controllers: [LocalAiProbeController] })
      .overrideProvider(PrismaService)
      .useValue(prismaMock)
      .overrideProvider(PrismaSystemService)
      .useValue({
        asSystem: () => prismaMock,
        runAsSystem: (_reason: string, fn: (tx: unknown) => unknown) => fn(prismaMock),
        $connect: async () => undefined,
        $disconnect: async () => undefined,
      })
      // The job worker would poll a database that is not there.
      .overrideProvider(JobWorker)
      .useValue({})
      .compile();
    app = moduleRef.createNestApplication<NestFastifyApplication>(new FastifyAdapter());
    await app.register(fastifyCookie, { secret: 'test-secret' });
    app.setGlobalPrefix('api');
    resetPrismaMock();
    installBaseMocks();
    await app.init();
    await app.getHttpAdapter().getInstance().ready();
    for (const cronJob of app.get(SchedulerRegistry).getCronJobs().values()) void cronJob.stop();
    jwt = moduleRef.get(JwtService);
  });

  afterAll(async () => {
    clearMockUsers();
    await app?.close();
  });

  async function call(path: string, token?: string): Promise<number> {
    const response = await app.inject({
      method: 'GET',
      url: `/api/localai-probe/${path}`,
      headers: token ? { authorization: `Bearer ${token}` } : {},
    });
    return response.statusCode;
  }

  it('answers 401 without a session', async () => {
    for (const route of ROUTES) expect(await call(route.path)).toBe(401);
  });

  it('gives a viewer 403 on lab:operate and 2xx on lab:read', async () => {
    const { accessToken } = registerMockUser(jwt, 'viewer');
    expect(await call('operate', accessToken)).toBe(403);
    expect(await call('read', accessToken)).toBe(200);
  });

  it('gives a contributor 2xx on lab:operate', async () => {
    const { accessToken } = registerMockUser(jwt, 'contributor');
    expect(await call('operate', accessToken)).toBe(200);
  });

  describe.each(Object.keys(ALLOWED) as FixtureRole[])('as %s', (role) => {
    it.each(ROUTES)('$permission', async ({ path, permission }) => {
      const { accessToken } = registerMockUser(jwt, role);
      const status = await call(path, accessToken);
      if (ALLOWED[role].includes(permission)) expect(status).toBe(200);
      else expect(status).toBe(403);
    });
  });
});
