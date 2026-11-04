// The roles and permissions this app seeds, as options for the platform's
// packaged registry (`@marinoscar/platform-api/manifest`): the permission sets
// of the slices this app mounts, in the platform's seed order, then the app's
// own. The identity slice's four roles always come first. Pure data, no side
// effect: `prisma/seed.ts` composes the catalog from it
// (`platformPermissionCatalog`) and `./registrations.ts` fills the registries
// with it (`registerPlatformPermissions`), so the two never disagree.
//
// The OPTIONAL slices bring their own: each enabled slice in
// `packages/shared/slices.json` names the permission sets it declares
// (`ApiSlice.permissionSlices`: storage, notifications, AI, backup), so
// the seed writes the rows its routes check. Re-run `npm run prisma:seed` after
// enabling one. A slice without permissions of its own (credentials, email,
// onboarding) enforces the `settings` slice's.
import type { PlatformPermissionOptions } from '@marinoscar/platform-api/manifest';

import { enabledPermissionSlices } from './slices/manifest';

export const PERMISSION_OPTIONS = {
  slices: ['identity', 'settings', 'jobs', 'nodes', 'user-data', ...enabledPermissionSlices()],
  // The app's own permission batches go here (`app: { permissions: [...] }`).
} satisfies PlatformPermissionOptions;
