import { NAVIGATION } from '../../config/navigation';

describe('the Lab destination', () => {
  const lab = NAVIGATION.destinations.find((destination) => destination.key === 'lab');

  it('is gated on lab:read, the permission GET /api/localai/nodes enforces', () => {
    expect(lab).toMatchObject({ path: '/lab', permission: 'lab:read', label: 'Lab' });
  });

  it('is a navigation destination, not a settings page: it has no settings card', () => {
    expect(NAVIGATION.destinationRoutes?.lab).toEqual(['/lab']);
    const cardPaths = (NAVIGATION.settingsSurfaces ?? []).flatMap((surface) => surface.sections.flatMap((section) => section.cards.map((card) => card.path)));
    expect(cardPaths).not.toContain('/lab');
  });
});
