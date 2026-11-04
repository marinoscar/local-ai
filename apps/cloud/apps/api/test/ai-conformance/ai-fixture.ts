// How this app builds itself for the platform's AI conformance suites
// (kill switch, RBAC matrix, secret egress, key policy, jobs server-only).
//
// The suites live in `@marinoscar/platform-api/ai/testing`; they cannot build
// the app, so this object is the whole of what they ask of it:
//   - the full `AppModule` over a MOCKED Prisma client (`./prisma.mock.ts`), with
//     the AI runtime swapped for the platform's harness (`createAiRuntimeHarness`:
//     the real `AiService`, runs and config over a fake provider and in-memory
//     tables), listening on an ephemeral port;
//   - users with the role grants this app seeds (`./mock-users.ts`);
//   - the OpenAPI document of the booted app, from which the routes and their
//     `x-rbac` metadata are DISCOVERED, never listed.
import type { AddressInfo } from 'node:net';

import fastifyCookie from '@fastify/cookie';
import multipart from '@fastify/multipart';
import { JwtService } from '@nestjs/jwt';
import { SchedulerRegistry } from '@nestjs/schedule';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { Test } from '@nestjs/testing';
import {
  AiConfigService,
  AiOutputWriter,
  AiRunsService,
  AiService,
  AiStorageInputResolver,
  type AiPolicy,
} from '@marinoscar/platform-api/ai';
import {
  HARNESS_OTHER_USER,
  createAiRuntimeHarness,
  type AiConformanceApp,
  type AiConformanceContext,
  type AiConformanceFixture,
  type AiRuntimeHarnessOptions,
} from '@marinoscar/platform-api/ai/testing';
import { createOpenApiDocument } from '@marinoscar/platform-api/host';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import { AppModule } from '../../src/app.module';
import { APP_OPENAPI } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, fixtureRolePermissions, installBaseMocks, registerMockUser, type FixtureRole } from './mock-users';
import { MOCK_DEFAULT_ORG_ID, prismaMock, resetPrismaMock } from './prisma.mock';

interface BootedContext extends AiConformanceContext {
  app: NestFastifyApplication;
  jwt: JwtService;
}

type ProviderOverride = { provide: unknown; useValue: unknown };

/** The full AppModule over the mocked database, the way `main.ts` builds it (cookie, multipart, `/api` prefix). */
async function createContext(options?: { overrideProviders?: ProviderOverride[] }): Promise<BootedContext> {
  let builder = Test.createTestingModule({ imports: [AppModule] })
    .overrideProvider(PrismaService)
    .useValue(prismaMock)
    // The bypass pool would open a second real connection; against a mock it is the same mock.
    .overrideProvider(PrismaSystemService)
    .useValue({
      asSystem: () => prismaMock,
      runAsSystem: (_reason: string, fn: (tx: unknown) => unknown) => fn(prismaMock),
      $connect: async () => undefined,
      $disconnect: async () => undefined,
    })
    // The job worker would poll a database that is not there.
    .overrideProvider(JobWorker)
    .useValue({});
  for (const { provide, useValue } of options?.overrideProviders ?? []) {
    builder = builder.overrideProvider(provide).useValue(useValue);
  }
  const moduleRef = await builder.compile();
  const app = moduleRef.createNestApplication<NestFastifyApplication>(new FastifyAdapter());
  await app.register(fastifyCookie, { secret: 'test-secret' });
  await app.register(multipart, { limits: { fileSize: 100 * 1024 * 1024, files: 1 } });
  app.setGlobalPrefix('api');
  installBaseMocks();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  // Stop every @Cron timer: they tick on the wall clock and none of these suites is about a scheduled tick.
  for (const cronJob of app.get(SchedulerRegistry).getCronJobs().values()) void cronJob.stop();
  return { app, prismaMock, jwt: moduleRef.get(JwtService) };
}

const BASE_POLICY: Pick<AiPolicy, 'enabled' | 'keyPolicy' | 'logPromptContent' | 'limits'> & { defaults: AiPolicy['defaults'] } = {
  enabled: true,
  keyPolicy: 'byok',
  logPromptContent: false,
  defaults: { allowBackgroundRuns: true, allowRealtime: false },
  limits: {},
};

/** The app with the AI harness wired in, listening on an ephemeral port. */
async function createAiApp(options: AiRuntimeHarnessOptions = {}): Promise<AiConformanceApp> {
  let current: Parameters<AiConformanceApp['script']>[0];
  const harness = createAiRuntimeHarness({
    ...options,
    fake: {
      ...options.fake,
      // Indirection, so each test can script the fake without a new app.
      responses: (req, ctx) => {
        if (typeof current === 'function') return current(req, ctx);
        if (Array.isArray(current)) {
          const next = current.shift();
          if (!next) throw new Error('script exhausted');
          return next;
        }
        return { outputText: `fake: ${typeof req.input === 'string' ? req.input : 'items'}` };
      },
    },
  });
  const context = await createContext({
    overrideProviders: [
      { provide: AiService, useValue: harness.ai },
      { provide: AiRunsService, useValue: harness.runs },
      { provide: AiConfigService, useValue: harness.aiConfig },
      { provide: AiStorageInputResolver, useValue: harness.inputs },
      { provide: AiOutputWriter, useValue: harness.outputs },
    ],
  });
  await context.app.listen(0, '127.0.0.1');
  const { port } = context.app.getHttpServer().address() as AddressInfo;
  const fakeOptions = (harness.fake as unknown as { options: { delayMs?: number } }).options;

  return {
    context,
    harness,
    baseUrl: `http://127.0.0.1:${port}`,
    script(next) {
      current = next;
    },
    setDelay(ms) {
      fakeOptions.delayMs = ms;
    },
    reset() {
      resetPrismaMock();
      clearMockUsers();
      installBaseMocks();
      current = undefined;
      fakeOptions.delayMs = 0;
      harness.fake.reset();
      harness.usageEvents.length = 0;
      harness.runRows.length = 0;
      harness.enqueued.length = 0;
      harness.storage.reset();
      harness.setOrgKey(null);
      harness.clearOrgPolicies();
      harness.clearTenantKeys();
      harness.clearAiConfigWriters();
      harness.removeUserKeys(HARNESS_OTHER_USER);
      harness.setPolicy({ ...BASE_POLICY, defaults: { ...BASE_POLICY.defaults }, limits: {} });
    },
    close: () => context.app.close(),
  };
}

export const aiConformanceFixture: AiConformanceFixture = {
  createAiApp,
  createContext,
  closeContext: (context) => (context as BootedContext).app.close(),
  async createUser(context, options) {
    const { id, accessToken } = registerMockUser((context as BootedContext).jwt, options.roleName as FixtureRole, options.id);
    return { id, accessToken };
  },
  openApiDocument: (app) => createOpenApiDocument(app as never, APP_OPENAPI) as never,
  defaultOrgId: MOCK_DEFAULT_ORG_ID,
  get rolePermissions() {
    return fixtureRolePermissions();
  },
};
