// The Alerts page over the wireframe's alerts: severity as icon + word, node, title, code and plain fix; the filters; Ack
// for lab:operate only (hidden for a viewer) and what happens when it succeeds or fails; the empty and failure states;
// and the axe checks. The transports and the permissions are stubbed.
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';
import { MemoryRouter } from 'react-router-dom';

import { ALERTS_NOW, alertsFixture, resolvedFixture } from './fixture';
import { AlertsPage } from './AlertsPage';
import type { AlertFilters, AlertsResponse } from './types';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

const NODES = [
  { nodeId: 'ai-n1', displayName: 'Node One' },
  { nodeId: 'ai-n2', displayName: 'Node Two' },
  { nodeId: 'ai-n3', displayName: 'Node Three' },
];

function renderPage(overrides: Partial<React.ComponentProps<typeof AlertsPage>> = {}) {
  const fetchAlerts = overrides.fetchAlerts ?? vi.fn(async (_filters: AlertFilters): Promise<AlertsResponse> => alertsFixture());
  const acknowledgeAlert = overrides.acknowledgeAlert ?? vi.fn(async () => alertsFixture().alerts[0]!);
  const view = render(
    <MemoryRouter>
      <AlertsPage fetchAlerts={fetchAlerts} acknowledgeAlert={acknowledgeAlert} fetchNodeOptions={async () => NODES} now={ALERTS_NOW} />
    </MemoryRouter>,
  );
  return { ...view, fetchAlerts, acknowledgeAlert };
}

async function loaded() {
  return screen.findByRole('list', { name: 'Alerts' });
}

describe('AlertsPage', () => {
  beforeEach(() => {
    permissions = ['lab:read'];
  });

  it('renders the title and one row per alert, in the order the API sent', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: 'Alerts' })).toBeInTheDocument();
    const list = await loaded();
    const rows = within(list).getAllByRole('listitem');
    expect(rows.map((row) => row.getAttribute('data-testid'))).toEqual([
      'alert-00000000-0000-4000-8000-000000000001',
      'alert-00000000-0000-4000-8000-000000000002',
      'alert-00000000-0000-4000-8000-000000000003',
      'alert-00000000-0000-4000-8000-000000000004',
    ]);
  });

  it('shows severity as icon and word (never colour alone), the node, title, code and the plain fix', async () => {
    renderPage();
    const list = await loaded();
    const first = within(list).getAllByRole('listitem')[0]!;
    expect(within(first).getByTestId('severity-chip')).toHaveTextContent('critical');
    expect(first.querySelector('[data-testid="severity-chip"] svg')).not.toBeNull();
    expect(first).toHaveTextContent('ai-n2');
    expect(first).toHaveTextContent('Ark drive not synced for 95 days');
    expect(within(first).getByTestId('alert-code')).toHaveTextContent('LAI-CLD-022');
    expect(first).toHaveTextContent('Plug the AI-ARK drive into the node and press "Sync now"');
    expect(first).toHaveTextContent('Raised 12 h ago');
    const words = within(list).getAllByTestId('severity-chip').map((chip) => chip.textContent);
    expect(words).toEqual(['critical', 'warning', 'warning', 'info']);
  });

  it('the stale-node alert is visible with its fix', async () => {
    renderPage();
    const list = await loaded();
    const stale = within(list).getByText('Not seen for 3 h').closest('li')!;
    expect(stale).toHaveTextContent('ai-n3');
    expect(stale).toHaveTextContent('LAI-CLD-020');
    expect(stale).toHaveTextContent('If it was moved to Offline mode on purpose, ignore this.');
  });

  it('asks for the active alerts of every node first, and again when a filter changes', async () => {
    const { fetchAlerts } = renderPage();
    await loaded();
    expect(fetchAlerts).toHaveBeenLastCalledWith({ state: 'active', nodeId: '' });

    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Show' }));
    fireEvent.click(await screen.findByRole('option', { name: 'Resolved' }));
    await waitFor(() => expect(fetchAlerts).toHaveBeenLastCalledWith({ state: 'cleared', nodeId: '' }));

    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Node' }));
    fireEvent.click(await screen.findByRole('option', { name: 'ai-n3' }));
    await waitFor(() => expect(fetchAlerts).toHaveBeenLastCalledWith({ state: 'cleared', nodeId: 'ai-n3' }));
  });

  it('a resolved alert says when it was resolved and has no Ack', async () => {
    permissions = ['lab:read', 'lab:operate'];
    renderPage({ fetchAlerts: vi.fn(async () => resolvedFixture()) });
    const list = await loaded();
    const row = within(list).getAllByRole('listitem')[0]!;
    expect(row).toHaveAttribute('data-state', 'cleared');
    expect(row).toHaveTextContent('Resolved 25 h ago');
    expect(within(row).queryByRole('button')).toBeNull();
  });

  it('an acknowledged alert says so and has no Ack', async () => {
    permissions = ['lab:read', 'lab:operate'];
    renderPage();
    const list = await loaded();
    const row = within(list).getAllByRole('listitem')[1]!;
    expect(row).toHaveTextContent('Acknowledged 3 h ago');
    expect(within(row).queryByRole('button', { name: /acknowledge/i })).toBeNull();
  });

  it('says there are no alerts, in words, when the list is empty', async () => {
    renderPage({ fetchAlerts: vi.fn(async () => ({ alerts: [] })) });
    expect(await screen.findByTestId('alerts-empty')).toHaveTextContent('No alerts');
    expect(screen.queryByRole('list', { name: 'Alerts' })).toBeNull();
  });

  describe('Ack', () => {
    it('is hidden for a viewer (lab:read only): no Ack button on any row', async () => {
      renderPage();
      await loaded();
      expect(screen.queryByRole('button', { name: /^(ack|acknowledge)/i })).toBeNull();
    });

    it('shows for lab:operate on the unacknowledged active alerts only, and acknowledges then refreshes', async () => {
      permissions = ['lab:read', 'lab:operate'];
      const { acknowledgeAlert, fetchAlerts } = renderPage();
      await loaded();
      const buttons = screen.getAllByRole('button', { name: /^Acknowledge:/ });
      // Rows 1, 3 and 4 are unacknowledged; row 2 already is.
      expect(buttons).toHaveLength(3);
      expect(buttons[0]).toHaveAccessibleName('Acknowledge: ai-n2 Ark drive not synced for 95 days');
      const callsBefore = (fetchAlerts as ReturnType<typeof vi.fn>).mock.calls.length;
      fireEvent.click(buttons[0]!);
      await waitFor(() => expect(acknowledgeAlert).toHaveBeenCalledWith('00000000-0000-4000-8000-000000000001'));
      await waitFor(() => expect((fetchAlerts as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(callsBefore));
    });

    it('says why when the cloud refuses, and keeps the list', async () => {
      permissions = ['lab:read', 'lab:operate'];
      renderPage({ acknowledgeAlert: vi.fn(async () => Promise.reject(new Error('This alert is already resolved.'))) });
      await loaded();
      fireEvent.click(screen.getAllByRole('button', { name: /^Acknowledge:/ })[0]!);
      expect(await screen.findByText('This alert is already resolved.')).toBeInTheDocument();
      expect(screen.getByRole('list', { name: 'Alerts' })).toBeInTheDocument();
    });
  });

  describe('failure states', () => {
    it('says it could not load the alerts, and offers to try again', async () => {
      const fetchAlerts = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue(alertsFixture());
      renderPage({ fetchAlerts });
      expect(await screen.findByText('Could not load the alerts.')).toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
      expect(await loaded()).toBeInTheDocument();
    });
  });

  describe('accessibility', () => {
    it('has no axe violations with alerts, as a viewer and as an operator', async () => {
      for (const granted of [['lab:read'], ['lab:read', 'lab:operate']]) {
        permissions = granted;
        const { container, unmount } = renderPage();
        await loaded();
        const results = await runAxe(container);
        expect(results.violations.map((violation) => `${violation.id}: ${violation.nodes.map((node) => node.html).join(' | ')}`)).toEqual([]);
        unmount();
      }
    });

    it('has no axe violations when empty', async () => {
      const { container } = renderPage({ fetchAlerts: vi.fn(async () => ({ alerts: [] })) });
      await screen.findByTestId('alerts-empty');
      const results = await runAxe(container);
      expect(results.violations).toEqual([]);
    });
  });
});
