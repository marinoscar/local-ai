// The app's composition of the platform: every packaged slice it mounts,
// configured once with `forRoot()`, and the host modules binding their ports.
// app.module.ts imports `PLATFORM_MODULES` and nothing else of the platform.
//
// The CORE (always mounted): the host core, identity, settings, worker-node
// credentials, jobs, the Doctor, the data resets. The OPTIONAL slices (storage,
// email, notifications, AI, backup, onboarding, telemetry) are
// `packages/shared/slices.json`: `./slices/` builds their modules, registers
// what they add to the static registries and overrides the host ports they
// replace, for the enabled ones only.
//
// The app's settings namespaces register FIRST: `SettingsModule.forRoot()`
// composes the system settings request bodies from the registry. (The host
// core's `maintenance` namespace is registered by `PlatformHostCoreModule.forRoot()`,
// first in PLATFORM_MODULES, so before `SettingsModule.forRoot()` too.)
import './storage.settings';
// The `localai` namespace (lab alert thresholds, catalog source, gateway defaults): src/localai/localai.settings.ts.
import '../localai/localai.settings';
// Everything the enabled slices add to the registries (settings namespaces,
// credential purposes, storage prefixes, notifications, onboarding steps...).
// Before the first `forRoot()` below, and never from `onModuleInit`.
import './slices/bootstrap';

import { APP_NAME } from '@app/shared';
import { Global, Module, type ModuleMetadata, type Provider } from '@nestjs/common';
import { PLATFORM_PRISMA, PlatformHostModule } from '@marinoscar/platform-api/core';
import { DoctorModule } from '@marinoscar/platform-api/doctor';
import { AppMetricsService, EVENT_BUS, PlatformHostCoreModule } from '@marinoscar/platform-api/host';
import {
  IDENTITY_EVENT_BUS,
  IDENTITY_JOBS,
  IDENTITY_METRICS,
  IDENTITY_NODE_CREDENTIALS,
  IDENTITY_NOTIFIER,
  IDENTITY_PROFILE_IMAGES,
  IdentityModule,
  USER_DEFAULTS,
} from '@marinoscar/platform-api/identity';
import { JOBS_EVENT_BUS, JOBS_METRICS, JobsModule } from '@marinoscar/platform-api/jobs';
import { NODE_JOB_INPUTS, NODE_OBJECT_STORE, NodeCredentialModule, NodeCredentialService, NodesModule } from '@marinoscar/platform-api/nodes';
import { SETTINGS_DATA, SETTINGS_PROFILE_IMAGES, SettingsModule } from '@marinoscar/platform-api/settings';

import { PrismaService } from '../prisma/prisma.service';
import {
  AppProfileImages,
  AppUserDefaults,
  IdentityJobsAdapter,
  LoggingIdentityNotifier,
  PrismaAuditSink,
  SettingsDataAdapter,
  SystemSettingsStoreAdapter,
} from './adapters';
import { platformHost } from './host';
import { NoNodeJobInputs, NoNodeObjectStore } from './nodes/nodes-host';
import { ENABLED, enabledSlices } from './slices/manifest';
import { userDataModule } from './user-data/user-data.config';

/** What a module can export: a provider token. */
type PortToken = NonNullable<ModuleMetadata['exports']>[number];

const CORE_PORTS: Array<Provider & { provide: unknown }> = [
  { provide: PLATFORM_PRISMA, useExisting: PrismaService },
  { provide: SETTINGS_DATA, useClass: SettingsDataAdapter },
  { provide: SETTINGS_PROFILE_IMAGES, useClass: AppProfileImages },
  { provide: IDENTITY_PROFILE_IMAGES, useClass: AppProfileImages },
  { provide: IDENTITY_NOTIFIER, useClass: LoggingIdentityNotifier },
  { provide: USER_DEFAULTS, useClass: AppUserDefaults },
  { provide: IDENTITY_JOBS, useClass: IdentityJobsAdapter },
  { provide: IDENTITY_NODE_CREDENTIALS, useExisting: NodeCredentialService },
  // The nodes slice's data plane has nothing to sign here (see ./nodes/nodes-host.ts).
  { provide: NODE_OBJECT_STORE, useClass: NoNodeObjectStore },
  { provide: NODE_JOB_INPUTS, useClass: NoNodeJobInputs },
  // The host core's bus and metrics (PlatformHostCoreModule, global): the
  // principal cache's cross-replica invalidation, the queue's wake-up, and the
  // `app.auth.*` / `app.jobs.*` instruments.
  { provide: IDENTITY_EVENT_BUS, useExisting: EVENT_BUS },
  { provide: IDENTITY_METRICS, useExisting: AppMetricsService },
  { provide: JOBS_EVENT_BUS, useExisting: EVENT_BUS },
  { provide: JOBS_METRICS, useExisting: AppMetricsService },
];

/**
 * The core's ports, with each enabled slice's replacements applied (a provider
 * of the same token wins: the storage slice's profile-image rule, the
 * notifications slice's identity notifier) and any port only a slice binds
 * appended.
 */
function bindPorts(): Array<Provider & { provide: unknown }> {
  const overrides = new Map<unknown, Provider & { provide: unknown }>();
  for (const slice of ENABLED) {
    for (const port of (slice.hostPorts?.(enabledSlices) ?? []) as Array<Provider & { provide: unknown }>) overrides.set(port.provide, port);
  }
  const bound = CORE_PORTS.map((port) => overrides.get(port.provide) ?? port);
  const known = new Set(CORE_PORTS.map((port) => port.provide));
  return [...bound, ...[...overrides.values()].filter((port) => !known.has(port.provide))];
}

const PORTS = bindPorts();

/** The slices' host ports, bound once and globally (a guard is built in every module that uses `@Auth()`). */
@Global()
@Module({ providers: PORTS, exports: PORTS.map((port) => port.provide as PortToken) })
export class AppHostModule {}

/**
 * The background queue: `jobs` and its admin routes, the worker and the
 * hygiene crons. Configured HERE, above `PLATFORM_MODULES`, on purpose:
 * `JobsModule.forRoot()` registers the `jobs` settings namespace (the
 * job-history purge's policy), and `SettingsModule.forRoot()` below composes
 * the `/api/system-settings` request bodies from the registry once. Moved
 * after it, `forRoot()` throws. A `NodesModule.forRoot()` (`nodes`) goes up
 * here too.
 */
export const jobsModule = JobsModule.forRoot({ appName: APP_NAME, imports: [AppHostModule] });

/**
 * The worker-node control plane: `POST /api/nodes/register`, heartbeat, claim and deregister (the routes the Local AI
 * agent uses as an EAB worker node, docs/specs/cloud-app.md section 3.6), the fleet admin routes and their sweeps.
 * The Local AI node routes (`/api/nodes/:id/localai/*`, src/localai/node-link) sit next to these and add the binding
 * guard. Configured here for the same reason as `jobsModule`: it registers the `nodes` settings namespace.
 */
export const nodesModule = NodesModule.forRoot({ imports: [AppHostModule] });

export const PLATFORM_MODULES = [
  // The API host core: the event bus (EVENT_BUS_ADAPTER), the platform's app
  // metrics, maintenance mode (`/api/admin/maintenance` and the only
  // APP_GUARD), the `{ data }` envelope, the request log line, the exception
  // filter and request ids. `/api/docs` is mounted at bootstrap (main.ts).
  PlatformHostCoreModule.forRoot(),
  // Sign-in, sessions, tokens, users, the allowlist and organizations, plus
  // the guards and decorators every route uses (`@Auth()`, `@Public()`).
  IdentityModule.forRoot({ imports: [AppHostModule], enableTestAuth: process.env.NODE_ENV !== 'production' }),
  // `/api/system-settings`, `/api/user-settings`, `/api/org-settings`.
  SettingsModule.forRoot({ imports: [AppHostModule] }),
  // The `nod_` worker-node credential family the identity guard accepts.
  NodeCredentialModule,
  jobsModule,
  nodesModule,
  // The optional slices that `packages/shared/slices.json` enables, in mount
  // order (each one's `forRoot()` is built here and nowhere else).
  ...ENABLED.flatMap((slice) => slice.modules?.(enabledSlices) ?? []),
  // `GET /api/admin/doctor`: every registered check, read-only.
  DoctorModule.forRoot({ host: platformHost }),
  // The Danger Zone (`/api/user-data/*`), the admin factory reset and organization
  // offboarding. The manifest in ./user-data/user-data.manifest.ts is the data.
  userDataModule,
  // AUDIT_SINK, SYSTEM_SETTINGS_STORE and PLATFORM_PRISMA for every slice.
  PlatformHostModule.forRoot({
    audit: { useClass: PrismaAuditSink },
    settings: { useClass: SystemSettingsStoreAdapter },
    prisma: { useExisting: PrismaService },
  }),
];

