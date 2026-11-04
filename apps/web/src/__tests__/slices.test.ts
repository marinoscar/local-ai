// The web side of the slice manifest (`packages/shared/slices.json`): the core
// alone, and each optional slice alone with only what it requires. Each
// composition imports the app's registries afresh with the list mocked, then
// checks what "drop a slice by removing one line" promises: its routes, cards,
// providers and shell parts exist exactly while it is enabled, every card has a
// route behind the same permission, and the Danger Zone stays last.
import { SLICE_CATALOG, SLICE_IDS, type SliceId } from '@app/shared';

import { ALL_WEB_SLICES } from '../slices/definitions';

const ids = [...SLICE_IDS];

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

async function compose(enabled: readonly SliceId[]) {
  vi.resetModules();
  vi.doMock('@app/shared', async () => {
    const actual = await vi.importActual<typeof import('@app/shared')>('@app/shared');
    return { ...actual, ENABLED_SLICES: actual.resolveSliceIds(enabled) };
  });
  const manifest = await import('../slices/manifest');
  const { ADMIN_SECTIONS } = await import('../config/adminSections');
  const { USER_SETTINGS_SECTIONS } = await import('../config/userSettingsSections');
  const { dangerZoneLastViolations } = await import('@marinoscar/platform-web/user-data/headless');
  const cards = [...ADMIN_SECTIONS, ...USER_SETTINGS_SECTIONS].flatMap((section) => section.cards);
  return {
    manifest,
    ADMIN_SECTIONS,
    USER_SETTINGS_SECTIONS,
    cards,
    dangerZoneLastViolations,
    cardPaths: new Set(cards.map((card) => card.path)),
    routes: manifest.sliceRoutes,
  };
}

/** The cards and routes slice `id` declares, from its definition. */
function declared(id: SliceId) {
  const slice = ALL_WEB_SLICES[id];
  return {
    cardPaths: [...(slice.adminCards ?? []), ...(slice.userCards ?? [])].flatMap((group) => group.cards.map((card) => card.path!)),
    routePaths: [...(slice.routes ?? []), ...(slice.publicRoutes ?? [])].map((route) => route.path),
  };
}

afterEach(() => {
  vi.doUnmock('@app/shared');
});

describe('the web slice definitions', () => {
  it('has one definition per slice in the catalog, under its own id', () => {
    expect(Object.keys(ALL_WEB_SLICES)).toEqual(ids);
    for (const id of ids) expect(ALL_WEB_SLICES[id].id).toBe(id);
  });

  it.each(ids.map((id) => [id]))('%s: every card it declares has a route behind the same permission', (id) => {
    const slice = ALL_WEB_SLICES[id as SliceId];
    const cards = [...(slice.adminCards ?? []), ...(slice.userCards ?? [])].flatMap((group) => group.cards);
    const routes = new Map((slice.routes ?? []).map((route) => [`/${route.path}`, route]));
    for (const card of cards) {
      const route = routes.get(card.path!);
      expect(route, `${card.title}: no route ${card.path}`).toBeDefined();
      expect(JSON.stringify(route!.permission ?? null), `${card.title}: route and card permissions differ`).toBe(JSON.stringify(card.permission ?? null));
    }
  });
});

describe('the core alone (no optional slice)', () => {
  it('has no slice route, card or shell part', async () => {
    const app = await compose([]);
    expect(app.routes).toEqual([]);
    expect(app.manifest.slicePublicRoutes).toEqual([]);
    expect(app.manifest.sliceShellProviders).toEqual([]);
    expect(app.manifest.sliceBanners).toEqual([]);
    expect(app.manifest.sliceOverlays).toEqual([]);
    for (const id of ids) for (const path of declared(id).cardPaths) expect(app.cardPaths.has(path)).toBe(false);
  });

  it('keeps the Danger Zone last in both hubs', async () => {
    const app = await compose([]);
    expect(app.dangerZoneLastViolations(app.ADMIN_SECTIONS, '/admin/settings/factory-reset')).toEqual([]);
    expect(app.dangerZoneLastViolations(app.USER_SETTINGS_SECTIONS, '/settings/danger-zone')).toEqual([]);
  });
});

describe.each(ids.map((id) => [id]))('the %s slice', (id) => {
  const enabled = closureOf(id as SliceId);

  it('adds its routes and cards, and the cards of no slice left out', async () => {
    const app = await compose(enabled);
    const routePaths = new Set([...app.routes.map((route) => route.path), ...app.manifest.slicePublicRoutes.map((route) => route.path)]);
    for (const on of enabled) {
      for (const path of declared(on).routePaths) expect(routePaths.has(path), `${on}: route ${path}`).toBe(true);
      for (const path of declared(on).cardPaths) expect(app.cardPaths.has(path), `${on}: card ${path}`).toBe(true);
    }
    for (const off of ids.filter((other) => !enabled.includes(other))) {
      for (const path of declared(off).routePaths) expect(routePaths.has(path), `${off}: route ${path}`).toBe(false);
      for (const path of declared(off).cardPaths) expect(app.cardPaths.has(path), `${off}: card ${path}`).toBe(false);
    }
  });

  it('routes every card it shows, behind a unique path, with the Danger Zone last', async () => {
    const app = await compose(enabled);
    const paths = app.cards.map((card) => card.path!);
    expect(new Set(paths).size).toBe(paths.length);
    expect(app.dangerZoneLastViolations(app.ADMIN_SECTIONS, '/admin/settings/factory-reset')).toEqual([]);
    expect(app.dangerZoneLastViolations(app.USER_SETTINGS_SECTIONS, '/settings/danger-zone')).toEqual([]);
    const routed = new Set(app.routes.map((route) => `/${route.path}`));
    // The core's own cards are routed in App.tsx; every slice card must be in the slices' routes.
    const sliceCardPaths = enabled.flatMap((on) => declared(on).cardPaths);
    for (const path of sliceCardPaths) expect(routed.has(path), `no route for ${path}`).toBe(true);
  });
});

describe('with every slice enabled', () => {
  it('has unique card and route paths and keeps the AI cards behind their feature', async () => {
    const app = await compose(ids);
    const routes = app.routes.map((route) => route.path);
    expect(new Set(routes).size).toBe(routes.length);
    const aiCards = app.cards.filter((card) => card.path?.includes('/ai'));
    expect(aiCards.length).toBeGreaterThan(0);
    // Every AI card hides while AI is off, except the one that switches it on.
    for (const card of aiCards) expect(card.feature === 'ai' || card.path === '/admin/settings/ai').toBe(true);
  });
});
