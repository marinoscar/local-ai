// API keys is a navigation destination gated on gateway:use (the permission GET /api/localai/gateway/keys enforces), and
// owns every /gateway route so the rail and the bottom bar keep it selected on the pages that follow (usage, models).
import { resolveActiveDestination, shellDestinationRoutes } from '@marinoscar/platform-web/shell/headless';

import { NAVIGATION } from '../../config/navigation';

describe('the API keys destination', () => {
  const gateway = NAVIGATION.destinations.find((destination) => destination.key === 'gateway');

  it('is gated on gateway:use and leads to /gateway/keys', () => {
    expect(gateway).toMatchObject({ label: 'API keys', path: '/gateway/keys', permission: 'gateway:use' });
  });

  it('owns the /gateway routes', () => {
    const routes = shellDestinationRoutes(NAVIGATION);
    expect(resolveActiveDestination(routes, '/gateway/keys')).toBe('gateway');
    expect(resolveActiveDestination(routes, '/gateway/usage')).toBe('gateway');
    expect(resolveActiveDestination(routes, '/gateway/models')).toBe('gateway');
    expect(resolveActiveDestination(routes, '/gatewayx')).toBeNull();
  });

  it('is a page, not a settings card', () => {
    const cardPaths = (NAVIGATION.settingsSurfaces ?? []).flatMap((surface) => surface.sections.flatMap((section) => section.cards.map((card) => card.path)));
    expect(cardPaths).not.toContain('/gateway/keys');
    expect(cardPaths).not.toContain('/gateway/models');
  });
});
