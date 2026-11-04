// The Lab page over the 3-node fixture: header counts, the gateway figure, one card per node, the Add node button for
// lab:admin only, the empty and failure states, and the axe checks. The transport and the permissions are stubbed.
import { render, screen, within } from '@testing-library/react';
import axe from 'axe-core';
import { MemoryRouter } from 'react-router-dom';

import { LAB_NOW, labFixture } from './fixture';
import { LabOverviewPage, summaryText } from './LabOverviewPage';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

function renderPage(fetchNodes: () => Promise<ReturnType<typeof labFixture>>) {
  return render(
    <MemoryRouter>
      <LabOverviewPage fetchNodes={fetchNodes} now={LAB_NOW} timeZone="UTC" />
    </MemoryRouter>,
  );
}

describe('LabOverviewPage', () => {
  beforeEach(() => {
    permissions = ['lab:read'];
  });

  it('renders the wireframe: title, counts, gateway figure and one card per node, in order', async () => {
    renderPage(async () => labFixture());
    expect(await screen.findByRole('heading', { level: 1, name: 'Lab' })).toBeInTheDocument();
    expect(await screen.findByTestId('lab-summary')).toHaveTextContent('3 nodes · 2 online · 1 offline mode · 2 alerts');
    expect(screen.getByTestId('gateway-chip')).toHaveTextContent('Gateway: 2 nodes');
    const list = screen.getByRole('list', { name: 'Nodes' });
    const cards = within(list).getAllByRole('article');
    expect(cards.map((card) => card.getAttribute('data-testid'))).toEqual(['node-card-ai-n1', 'node-card-ai-n2', 'node-card-ai-n3']);
    expect(within(list).getAllByRole('link', { name: /^Open ai-n/ }).map((link) => link.getAttribute('href'))).toEqual([
      '/lab/nodes/ai-n1',
      '/lab/nodes/ai-n2',
      '/lab/nodes/ai-n3',
    ]);
  });

  it('shows a viewer (lab:read only) no action buttons except the per-card Open links', async () => {
    renderPage(async () => labFixture());
    await screen.findByTestId('lab-summary');
    expect(screen.queryByRole('link', { name: 'Add node' })).toBeNull();
    expect(screen.queryByRole('button', { name: /add node|retire|remove|delete/i })).toBeNull();
  });

  it('shows lab:admin an Add node link to /lab/nodes/new', async () => {
    permissions = ['lab:read', 'lab:admin'];
    renderPage(async () => labFixture());
    const add = await screen.findByRole('link', { name: 'Add node' });
    expect(add).toHaveAttribute('href', '/lab/nodes/new');
  });

  it('says "No nodes yet" with an Add node button for an admin and a hint for a viewer', async () => {
    const empty = labFixture();
    empty.nodes = [];
    empty.summary = { nodes: 0, online: 0, offlineMode: 0, activeAlerts: 0, routable: 0 };
    permissions = ['lab:read', 'lab:admin'];
    const admin = renderPage(async () => empty);
    expect(await screen.findByTestId('lab-empty')).toHaveTextContent('No nodes yet');
    expect(within(screen.getByTestId('lab-empty')).getByRole('link', { name: 'Add node' })).toHaveAttribute('href', '/lab/nodes/new');
    expect(screen.getByTestId('gateway-chip')).toHaveTextContent('Gateway: 0 nodes');
    admin.unmount();

    permissions = ['lab:read'];
    renderPage(async () => empty);
    expect(await screen.findByTestId('lab-empty')).toHaveTextContent('An administrator can add the first node.');
    expect(screen.queryByRole('link', { name: 'Add node' })).toBeNull();
  });

  it('says "Gateway: 1 node" in the singular and "1 alert" in the header', async () => {
    const one = labFixture();
    one.summary = { nodes: 1, online: 1, offlineMode: 0, activeAlerts: 1, routable: 1 };
    renderPage(async () => one);
    expect(await screen.findByTestId('gateway-chip')).toHaveTextContent('Gateway: 1 node');
    expect(screen.getByTestId('lab-summary')).toHaveTextContent('1 node · 1 online · 0 offline mode · 1 alert');
  });

  it('shows a failure with a retry when nothing loaded', async () => {
    renderPage(async () => {
      throw new Error('boom');
    });
    expect(await screen.findByText('Could not load the lab.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();
    expect(screen.queryByTestId('lab-summary')).toBeNull();
  });

  it('has no axe violations with the 3-node fixture, for a viewer and an admin', async () => {
    for (const held of [['lab:read'], ['lab:read', 'lab:admin']]) {
      permissions = held;
      const { container, unmount } = renderPage(async () => labFixture());
      await screen.findByTestId('lab-summary');
      const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
      expect(result.violations.map((violation) => `${violation.id}: ${violation.help}`)).toEqual([]);
      unmount();
    }
  });
});

describe('summaryText', () => {
  it('pluralises nodes and alerts', () => {
    expect(summaryText({ nodes: 3, online: 2, offlineMode: 1, activeAlerts: 2, routable: 2 })).toBe('3 nodes · 2 online · 1 offline mode · 2 alerts');
    expect(summaryText({ nodes: 1, online: 0, offlineMode: 0, activeAlerts: 1, routable: 0 })).toBe('1 node · 0 online · 0 offline mode · 1 alert');
    expect(summaryText({ nodes: 0, online: 0, offlineMode: 0, activeAlerts: 0, routable: 0 })).toBe('0 nodes · 0 online · 0 offline mode · 0 alerts');
  });
});
