// Each optional slice, composed ALONE with only what it requires, against the
// real module graph (no database: the container is compiled, not started).
// This is the per-slice test: it proves a slice mounts, its provider graph
// resolves, and that the slices left out are really absent, so removing one
// line of `packages/shared/slices.json` leaves a working app.
//
// Every composition runs in its own module registry (`jest.isolateModules`):
// the platform's registries are static and a slice registers into them at
// import time, so two compositions cannot share a process any other way.
import type { TestingModule } from '@nestjs/testing';

import { SLICE_CATALOG, SLICE_IDS, type SliceId } from '@app/shared';

/** A class that only an enabled slice provides, to probe for. Loaded inside the isolated registry. */
const PROBES: Readonly<Record<SliceId, { module: string; name: string }>> = {
  credentials: { module: '@marinoscar/platform-api/credentials', name: 'UserCredentialsService' },
  storage: { module: '@marinoscar/platform-api/storage', name: 'ObjectsService' },
  email: { module: '@marinoscar/platform-api/email', name: 'EmailSettingsService' },
  notifications: { module: '@marinoscar/platform-api/notifications', name: 'NotificationsService' },
  ai: { module: '@marinoscar/platform-api/ai', name: 'AiService' },
  'db-backup': { module: '@marinoscar/platform-api/db-backup', name: 'DatabaseBackupRunnerService' },
  onboarding: { module: '@marinoscar/platform-api/onboarding', name: 'OnboardingService' },
  telemetry: { module: '@marinoscar/platform-api/telemetry', name: 'TelemetryQueryService' },
};

/** A permission only that slice's routes check: seeded exactly while the slice is on. */
const SEEDED_PERMISSION: Partial<Record<SliceId, string>> = {
  storage: 'storage_config:read',
  notifications: 'push:read',
  ai: 'ai_config:read',
  'db-backup': 'db_backup:read',
  telemetry: 'telemetry:read',
};

function closureOf(id: SliceId): SliceId[] {
  const closure = new Set<SliceId>();
  const visit = (next: SliceId): void => {
    if (closure.has(next)) return;
    closure.add(next);
    SLICE_CATALOG[next].requires.forEach(visit);
  };
  visit(id);
  return [...closure];
}

interface Composition {
  moduleRef: TestingModule;
  has(id: SliceId): boolean;
  permissionIds(): string[];
  /** The class bound to a token of the global host module (identity's notifier, profile pictures). */
  bound(token: unknown): string;
  readonly identityNotifierToken: unknown;
  readonly profileImagesToken: unknown;
}

async function compose(enabled: readonly SliceId[]): Promise<Composition> {
  let load!: () => Promise<Composition>;
  jest.isolateModules(() => {
    jest.doMock('@app/shared', () => {
      const actual = jest.requireActual('@app/shared') as typeof import('@app/shared');
      // As `slices.json` would be: validated, in mount order.
      return { ...actual, ENABLED_SLICES: actual.resolveSliceIds(enabled) };
    });
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const { Test } = require('@nestjs/testing') as typeof import('@nestjs/testing');
    const { AppModule } = require('../src/app.module') as typeof import('../src/app.module');
    const { PERMISSION_OPTIONS } = require('../src/platform/permissions') as typeof import('../src/platform/permissions');
    const { platformPermissionCatalog } = require('@marinoscar/platform-api/manifest') as typeof import('@marinoscar/platform-api/manifest');
    // The probe classes are resolved HERE, in the same isolated registry as AppModule's own imports.
    const providers = Object.fromEntries(
      SLICE_IDS.map((id) => [id, (require(PROBES[id].module) as Record<string, new (...args: never[]) => unknown>)[PROBES[id].name]!]),
    ) as Record<SliceId, new (...args: never[]) => unknown>;
    const identity = require('@marinoscar/platform-api/identity') as typeof import('@marinoscar/platform-api/identity');
    load = async () => {
      const moduleRef = await Test.createTestingModule({ imports: [AppModule] }).compile();
      return {
        moduleRef,
        has: (id) => {
          try {
            return moduleRef.get(providers[id], { strict: false }) !== undefined;
          } catch {
            return false;
          }
        },
        identityNotifierToken: identity.IDENTITY_NOTIFIER,
        profileImagesToken: identity.IDENTITY_PROFILE_IMAGES,
        bound: (token) => (moduleRef.get(token as never, { strict: false }) as object).constructor.name,
        permissionIds: () => platformPermissionCatalog(PERMISSION_OPTIONS).permissions.map((permission) => permission.name),
      };
    };
  });
  return load();
}

describe('the core alone (no optional slice)', () => {
  let app: Composition;
  beforeAll(async () => {
    app = await compose([]);
  });
  afterAll(() => app?.moduleRef.close());

  it('mounts none of the optional slices', () => {
    expect(SLICE_IDS.filter((id) => app.has(id))).toEqual([]);
  });

  it('seeds none of their permissions', () => {
    expect(app.permissionIds()).not.toContain('storage_config:read');
    expect(app.permissionIds()).not.toContain('ai_config:read');
    expect(app.permissionIds()).toContain('system_settings:read');
  });
});

describe.each(SLICE_IDS.map((id) => [id]))('the %s slice', (id) => {
  const enabled = closureOf(id as SliceId);
  let app: Composition;
  beforeAll(async () => {
    app = await compose(enabled);
  });
  afterAll(() => app?.moduleRef.close());

  it(`mounts with ${enabled.length === 1 ? 'nothing else' : enabled.filter((e) => e !== id).join(', ')}`, () => {
    expect(app.has(id as SliceId)).toBe(true);
  });

  it('leaves every slice it does not require out', () => {
    expect(SLICE_IDS.filter((other) => !enabled.includes(other) && app.has(other))).toEqual([]);
  });

  it('seeds the permissions its routes check, and only for the slices that are on', () => {
    const seeded = app.permissionIds();
    for (const [slice, permission] of Object.entries(SEEDED_PERMISSION)) {
      expect(seeded.includes(permission)).toBe(enabled.includes(slice as SliceId));
    }
  });
});

describe('the host ports a slice replaces', () => {
  it('logs the identity notices until the notifications slice turns them into mail', async () => {
    const without = await compose([]);
    const withNotifications = await compose(closureOf('notifications'));
    // The token is read from the same isolated registry the composition used.
    expect(without.bound(without.identityNotifierToken)).toBe('LoggingIdentityNotifier');
    expect(withNotifications.bound(withNotifications.identityNotifierToken)).toBe('NotificationsIdentityNotifier');
    await without.moduleRef.close();
    await withNotifications.moduleRef.close();
  });

  it('knows uploaded avatars only once the storage slice is on', async () => {
    const without = await compose([]);
    const withStorage = await compose(closureOf('storage'));
    expect(without.bound(without.profileImagesToken)).toBe('AppProfileImages');
    expect(withStorage.bound(withStorage.profileImagesToken)).toBe('StorageProfileImages');
    await without.moduleRef.close();
    await withStorage.moduleRef.close();
  });
});
