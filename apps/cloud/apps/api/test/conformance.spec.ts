// The platform's invariants, run against THIS app (`runPlatformConformance`).
// The package owns each scan; the app supplies only its own data. A suite
// that ships with a slice registers when its testing entry is imported, and
// runs only when this file gives it options: the suites of an OPTIONAL slice
// (`packages/shared/slices.json`) get theirs only while that slice is enabled.
import '@marinoscar/platform-api/identity/testing';
import '@marinoscar/platform-api/host/testing';
import '@marinoscar/platform-api/user-data/testing';
import '@marinoscar/platform-api/credentials/testing';
import '@marinoscar/platform-api/storage/testing';
import '@marinoscar/platform-api/email/testing';
import '@marinoscar/platform-api/notifications/testing';
import '@marinoscar/platform-api/ai/testing';
import '@marinoscar/platform-api/db-backup/testing';
import '@marinoscar/platform-api/onboarding/testing';
import '@marinoscar/platform-api/telemetry/testing';

import { join } from 'node:path';
import {
  buildPermissionCatalog,
  catalogGrants,
  permissionRegistry,
  roleRegistry,
  userOwnedModelRegistry,
} from '@marinoscar/platform-api/core';
import { platformPermissionCatalog } from '@marinoscar/platform-api/manifest';
import { readSchemaDatamodel, runPlatformConformance } from '@marinoscar/platform-api/testing';

// Fills the registries: the platform's entries, then the app's.
import '../src/platform/registrations';
import { AppModule } from '../src/app.module';
import { LOCALAI_RAW_SQL } from '../src/localai/localai.raw-sql';
import { PERMISSION_OPTIONS } from '../src/platform/permissions';
import { ALL_SLICES } from '../src/platform/slices/definitions';
import { isSliceEnabled } from '../src/platform/slices/manifest';

const SOURCE_ROOT = join(__dirname, '..', 'src');
const API_ROOT = join(__dirname, '..');
const WEB_SOURCE_ROOT = join(API_ROOT, '..', 'web', 'src');
const MANIFESTS = [join(API_ROOT, 'package.json'), join(API_ROOT, '..', 'web', 'package.json'), join(API_ROOT, '..', '..', 'package.json')];

/** The suites of the optional slices that are on. */
function sliceSuites() {
  return {
    ...(isSliceEnabled('credentials') ? { credentials: { appOwners: ['localai'] } } : {}),
    ...(isSliceEnabled('storage')
      ? { storage: { requiredPrefixIds: isSliceEnabled('db-backup') ? ['database-backups'] : [] } }
      : {}),
    ...(isSliceEnabled('email')
      ? { email: { samples: { 'org-invitation': { recipientEmail: 'guest@example.test', orgName: 'Org Kappa', roleName: 'viewer' } } } }
      : {}),
    ...(isSliceEnabled('notifications') ? { notifications: { afterCommitAllowlist: [] } } : {}),
    ...(isSliceEnabled('ai')
      ? {
          // The five suites that boot the app (kill switch, RBAC matrix, secret egress, key policy, jobs
          // server-only) run in `ai-conformance.spec.ts`, over a mocked database; the two source scans are here.
          // No provider SDK anywhere in the app or the web (AI rule 1).
          aiNoSdkLeak: {
            apiTrees: [{ name: 'apps/api/src', root: SOURCE_ROOT, minFiles: 20 }],
            webTrees: [{ name: 'apps/web/src', root: WEB_SOURCE_ROOT, minFiles: 5 }],
            noSdkManifests: MANIFESTS,
          },
          aiOrchestrationBoundary: {
            apiSourceRoots: [SOURCE_ROOT],
            webSourceRoots: [WEB_SOURCE_ROOT],
            packageJsonPaths: MANIFESTS,
            allowedRoots: {},
            minApiFiles: 20,
          },
        }
      : {}),
    ...(isSliceEnabled('db-backup') ? { dbBackup: {} } : {}),
    ...(isSliceEnabled('onboarding')
      ? { onboarding: { permissions: permissionRegistry.list(), factSamples: {}, minSteps: 3 } }
      : {}),
    // The metric groups are the registry's: the platform's six plus the app's (`activity`), registered by
    // `TelemetryModule.forRoot` when AppModule is imported above. The slice's own cron lives in the package,
    // outside this app's source roots, so `cronSourceRoots` is left out.
    ...(isSliceEnabled('telemetry') ? { telemetry: {} } : {}),
  };
}

runPlatformConformance({
  sourceRoots: [SOURCE_ROOT],
  suites: {
    // Every @Cron only enqueues (CLAUDE.md queue rule 1). The app's first @Cron is the command sweeper
    // (src/localai/commands/sweeper.task.ts, CLOUD-08), which only enqueues the `localai.commands.sweep` job; the
    // suite refuses minCronFiles: 0 (a vacuous scan), so it now runs with no exemption.
    cronEnqueueOnly: { exempt: [], minCronFiles: 1 },
    // Every foreign key to User in the composed schema (the platform's models
    // and the app's fragments) has a registry entry whose purge matches
    // onDelete: the platform's inventory and the app's, as registered.
    userOwnedData: {
      schemaPath: join(__dirname, '..', 'prisma', 'schema'),
      policies: userOwnedModelRegistry.list(),
      // The raw SQL of every slice's code that is in the tree, enabled or not
      // (the scan reads every file under src/), each argued (`ApiSlice.rawSql`).
      rawSqlAllowlist: [...Object.values(ALL_SLICES).flatMap((slice) => slice.rawSql ?? []), ...LOCALAI_RAW_SQL],
      registerIn: 'src/app.ownership.ts (or your module\'s own *.ownership.ts)',
    },
    // Every route reachable from AppModule declares @Auth() or @Public(), and
    // every permission it names is one the seed writes.
    identity: {
      rootModule: AppModule,
      permissions: permissionRegistry.list(),
      roles: roleRegistry.list(),
      grants: catalogGrants(buildPermissionCatalog()),
      minRoutes: 4,
    },
    // The host core: exactly one APP_GUARD and it is the maintenance guard
    // (no global JWT guard), one PlatformHostCoreModule, the `{ data }`
    // envelope and the exception filter registered once.
    host: { rootModule: AppModule },
    // Every user-owned model has a keep-or-delete decision in src/platform/user-data/user-data.manifest.ts.
    userData: { datamodel: readSchemaDatamodel(join(__dirname, '..', 'prisma', 'schema')) },
    ...sliceSuites(),
  },
});

// What the seed writes (`prisma/seed.ts` composes the catalog from the same
// options) is exactly what the registries hold.
describe('the seeded permission catalog', () => {
  it('equals the registries the app registered', () => {
    expect(platformPermissionCatalog(PERMISSION_OPTIONS)).toEqual(buildPermissionCatalog());
  });
});
