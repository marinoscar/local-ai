// @vitest-environment node
// The web app's platform conformance (#570): the web counterpart of `apps/api/test/conformance.spec.ts`. The invariants
// the platform enforces over what this app DECLARES (its two settings registries and its routes) run here through
// `runPlatformWebConformance()`, so the suite ids and their cases are the package's, not a copy.
//
// What the suites read is the LIVE data:
//   - the registries are the arrays the hubs render (the core's cards plus the enabled slices', `src/slices/<id>.tsx`);
//   - the routes are the core's `<Route>` elements parsed from `App.tsx` plus the enabled slices' route tables;
//   - the permission ids are the catalog the API seeds (`apps/api/test/fixtures/permission-catalog.json`, which
//     `apps/api/test/permission-catalog-fixture.spec.ts` proves equal to the seed), so a card can only name a
//     permission the API declares.
//
// `settings-ai-cards` pins AI rule 5: every AI card (the slice's and the app's own "Use AI for music") carries an AI
// permission and `feature: 'ai'`, except the admin AI card that switches AI on.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { runPlatformWebConformance } from '@marinoscar/platform-web/testing';
// Importing the slice's testing entry REGISTERS its suites with the harness.
import { parseAppRoutes } from '@marinoscar/platform-web/settings/testing';
import { factoryResetSettingsPage } from '@marinoscar/platform-web/user-data/ui';

import catalog from '../../../api/test/fixtures/permission-catalog.json';
import { ADMIN_SECTIONS } from '../config/adminSections';
import { USER_SETTINGS_SECTIONS } from '../config/userSettingsSections';
import { isSliceEnabled, sliceRoutes } from '../slices/manifest';

const appTsx = readFileSync(fileURLToPath(new URL('../App.tsx', import.meta.url)), 'utf8');

// App.tsx writes nested routes relative (`admin/settings/users`); the cards name absolute paths. The factory reset's
// gate is an expression in App.tsx (the packaged card's own permission), which the source parser cannot read.
const absolute = (path: string) => (path.startsWith('/') ? path : `/${path}`);
const routes = [
  ...parseAppRoutes(appTsx).map((route) =>
    absolute(route.path) === factoryResetSettingsPage.card.path ? { path: absolute(route.path), permission: factoryResetSettingsPage.card.permission ?? null } : { ...route, path: absolute(route.path) },
  ),
  ...sliceRoutes.map((route) => ({ path: `/${route.path}`, permission: route.permission ?? null })),
];

runPlatformWebConformance({
  adminSections: ADMIN_SECTIONS,
  userSettingsSections: USER_SETTINGS_SECTIONS,
  hubs: {
    admin: { path: '/admin/settings', title: 'Administration' },
    user: { path: '/settings', title: 'Settings' },
  },
  routes,
  apiPermissions: catalog.permissions.map((permission) => permission.name),
  suites: {
    'settings-route-ownership': {
      skip: 'This app has no destination-to-route table: its navigation is the ShellNavigation model in src/config/navigation.ts, whose routes src/__tests__/registries.test.ts checks.',
    },
    ...(isSliceEnabled('ai') ? {} : { 'settings-ai-cards': { skip: 'The ai slice is not enabled (packages/shared/slices.json), so there is no AI card.' } }),
  },
});
