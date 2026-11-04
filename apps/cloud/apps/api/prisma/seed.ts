// `npm run prisma:seed`: the platform's rows first (roles, permissions and
// their default grants, the `global` system settings row, the initial
// administrator's allowlist entry), then the app's. Upserts only, so it runs
// on every deploy. Standalone (no Nest container): the permission catalog is
// composed from the platform's packaged registry plus the app's own
// (`platformPermissionCatalog`, the same options `src/platform/registrations.ts`
// registers), and the settings defaults from the settings registry.
import { PrismaPg } from '@prisma/adapter-pg';
import { PrismaClient } from '@prisma/client';
import { platformPermissionCatalog } from '@marinoscar/platform-api/manifest';
import { composeDefaultSystemSettings } from '@marinoscar/platform-api/settings';
import { platformSeedInputFrom, seedPlatform } from '@marinoscar/platform-db/seed';

import { PERMISSION_OPTIONS } from '../src/platform/permissions';
import { registerSliceSettings } from '../src/platform/slices/register';

// The enabled slices' settings namespaces, so the seeded `global` row carries their defaults.
registerSliceSettings();

async function main(): Promise<void> {
  const url = process.env.DATABASE_URL;
  if (!url) throw new Error('DATABASE_URL is not set: run `npm run prisma:seed`, which builds it from POSTGRES_*.');
  const prisma = new PrismaClient({ adapter: new PrismaPg(url) });
  try {
    const input = platformSeedInputFrom(
      { permissions: platformPermissionCatalog(PERMISSION_OPTIONS), settings: composeDefaultSystemSettings() as Record<string, unknown> },
      process.env,
    );
    await seedPlatform(prisma, input, { info: (msg) => console.log(msg) });
    await seedApp(prisma);
  } finally {
    await prisma.$disconnect();
  }
}

/** The app's own rows: none yet. Add reference data here (upserts only). */
async function seedApp(_prisma: PrismaClient): Promise<void> {}

main().catch((error: unknown) => {
  console.error('Seed failed:', error);
  process.exitCode = 1;
});
