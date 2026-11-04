// /lab/catalog is part of the Lab destination (a navigation destination, not a settings card), gated on lab:read, and
// reachable from the Lab page.
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { resolveActiveDestination, shellDestinationRoutes } from '@marinoscar/platform-web/shell/headless';

import { NAVIGATION } from '../../config/navigation';
import { labFixture } from '../lab/fixture';
import { LabOverviewPage } from '../lab/LabOverviewPage';

vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permission === 'lab:read' }),
}));

describe('the Catalog route', () => {
  it('belongs to the Lab destination, so the rail and the bottom bar keep Lab selected', () => {
    const routes = shellDestinationRoutes(NAVIGATION);
    expect(resolveActiveDestination(routes, '/lab/catalog')).toBe('lab');
    expect(resolveActiveDestination(routes, '/lab')).toBe('lab');
    expect(resolveActiveDestination(routes, '/labs')).toBeNull();
  });

  it('is not a settings page: no card points at it', () => {
    const cardPaths = (NAVIGATION.settingsSurfaces ?? []).flatMap((surface) => surface.sections.flatMap((section) => section.cards.map((card) => card.path)));
    expect(cardPaths).not.toContain('/lab/catalog');
  });

  it('is linked from the Lab page for anyone who can read it, viewers included', async () => {
    render(
      <MemoryRouter>
        <LabOverviewPage fetchNodes={async () => labFixture()} />
      </MemoryRouter>,
    );
    expect(await screen.findByRole('link', { name: 'Catalog' })).toHaveAttribute('href', '/lab/catalog');
  });
});
