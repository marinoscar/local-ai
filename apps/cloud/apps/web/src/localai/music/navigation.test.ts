// /music is its own destination (E11, #514): "Music" / "Música", gated by the permission the API enforces, and not a
// settings card.
import { resolveActiveDestination, shellDestinationRoutes } from '@marinoscar/platform-web/shell/headless';

import { NAVIGATION, localizedNavigation } from '../../config/navigation';

describe('the Music destination', () => {
  it('is /music, gated by localai_music:use, and the route belongs to it', () => {
    const music = NAVIGATION.destinations.find((destination) => destination.key === 'music');
    expect(music).toMatchObject({ path: '/music', permission: 'localai_music:use', label: 'Music' });
    const routes = shellDestinationRoutes(NAVIGATION);
    expect(resolveActiveDestination(routes, '/music')).toBe('music');
    expect(resolveActiveDestination(routes, '/musical')).toBeNull();
  });

  it('is "Música" in Spanish; only the words change', () => {
    const spanish = localizedNavigation('es');
    expect(spanish.destinations.find((destination) => destination.key === 'music')).toMatchObject({ label: 'Música', compactLabel: 'Música', path: '/music', permission: 'localai_music:use' });
    expect(spanish.destinations.map((destination) => [destination.key, destination.path])).toEqual(NAVIGATION.destinations.map((destination) => [destination.key, destination.path]));
    expect(localizedNavigation('en')).toBe(NAVIGATION);
  });

  it('is not a settings page: no card points at it', () => {
    const cardPaths = (NAVIGATION.settingsSurfaces ?? []).flatMap((surface) => surface.sections.flatMap((section) => section.cards.map((card) => card.path)));
    expect(cardPaths).not.toContain('/music');
  });
});
