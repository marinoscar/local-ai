import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { dangerZoneLastViolations } from '@marinoscar/platform-web/user-data/headless';

import { ADMIN_SECTIONS } from '../config/adminSections';
import { NAVIGATION } from '../config/navigation';
import { USER_SETTINGS_SECTIONS } from '../config/userSettingsSections';
import { sliceRoutes } from '../slices/manifest';

// The Settings UI Pattern, as tests: every card is reachable (App.tsx has its
// route). The packaged cards are checked by the platform's own suites.
const read = (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), 'utf8');
const appSource = read('../App.tsx');
// Every path the app routes: the core's literal routes in App.tsx and the enabled slices' (src/slices/<id>.tsx).
const routedPaths = new Set([...[...appSource.matchAll(/path="([^"]+)"/g)].map((match) => match[1]!), ...sliceRoutes.map((route) => route.path)]);
const cards = [...ADMIN_SECTIONS, ...USER_SETTINGS_SECTIONS].flatMap((section) => section.cards);

describe('settings registries', () => {
  it('give every card a unique path', () => {
    const paths = cards.map((card) => card.path);
    expect(paths.every(Boolean)).toBe(true);
    expect(new Set(paths).size).toBe(paths.length);
  });

  it.each(cards.map((card) => [card.path!]))('route %s exists (App.tsx or an enabled slice)', (path) => {
    expect(routedPaths.has(path.replace(/^\//, ''))).toBe(true);
  });

  it('keep the Danger Zone group last in both hubs', () => {
    expect(dangerZoneLastViolations(ADMIN_SECTIONS, '/admin/settings/factory-reset')).toEqual([]);
    expect(dangerZoneLastViolations(USER_SETTINGS_SECTIONS, '/settings/danger-zone')).toEqual([]);
  });
});

// The shell's navigation (the rail, the bottom bar, the user menu) reaches the
// same routes, behind the same permissions, as the cards.
describe('shell navigation', () => {
  it.each(NAVIGATION.destinations.filter((d) => d.path !== '/').map((d) => [d.path]))('destination %s has a route', (path) => {
    expect(routedPaths.has(path.replace(/^\//, ''))).toBe(true);
  });

  it('fit the bottom bar (four destinations at most)', () => {
    expect(NAVIGATION.destinations.length).toBeLessThanOrEqual(4);
  });
});
