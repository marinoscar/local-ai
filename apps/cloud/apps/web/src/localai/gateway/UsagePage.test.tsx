// The Usage page: the rows of the wireframe (day, bar, requests, tokens, node shares), the group-by and range controls,
// the admin scope, empty and error states, and the axe checks. The transports and the permissions are stubbed.
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';
import { MemoryRouter } from 'react-router-dom';

import { keysFixture } from './fixture';
import { emptyUsage, USAGE_NOW, usageByDay, usageByModel } from './usageFixture';
import type { UsageFilters, UsageResponse } from './usageTypes';
import { UsagePage, type UsagePageProps } from './UsagePage';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

function renderPage(overrides: UsagePageProps = {}) {
  const fetchUsage = (overrides.fetchUsage ?? vi.fn(async (filters: UsageFilters): Promise<UsageResponse> => (filters.groupBy === 'model' ? usageByModel() : usageByDay()))) as ReturnType<typeof vi.fn> &
    NonNullable<UsagePageProps['fetchUsage']>;
  const view = render(
    <MemoryRouter initialEntries={['/gateway/usage']}>
      <UsagePage fetchUsage={fetchUsage} listKeys={async () => keysFixture()} now={USAGE_NOW} {...overrides} />
    </MemoryRouter>,
  );
  return { ...view, fetchUsage };
}

const table = (name: string) => screen.findByRole('table', { name });
const axeRun = async () => (await runAxe(document.body)).violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html).join(' | ')}`);

describe('UsagePage', () => {
  beforeEach(() => {
    permissions = ['gateway:use'];
  });

  it('asks for the last 30 days grouped by day and shows the rows newest first, with bar, requests, tokens and node shares', async () => {
    const { fetchUsage } = renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: 'Usage' })).toBeInTheDocument();
    const rows = within(await table('Usage by day')).getAllByRole('row').slice(1);
    expect(fetchUsage).toHaveBeenCalledWith({ days: 30, groupBy: 'day', keyId: null, all: false }, USAGE_NOW);
    expect(rows.map((row) => within(row).getAllByRole('cell')[0]!.textContent?.slice(0, 10))).toEqual(['2026-11-04', '2026-11-03', '2026-11-02']);
    const busy = screen.getByTestId('usage-row-2026-11-02');
    expect(busy).toHaveTextContent('412 requests');
    expect(busy).toHaveTextContent('1.1M tokens');
    expect(busy).toHaveTextContent('1M in · 120k out');
    expect(within(busy).getByTestId('usage-shares')).toHaveTextContent('ai-n1 61 % · ai-n2 30 % · ai-n3 9 %');
    expect(screen.getByTestId('usage-row-2026-11-04')).toHaveTextContent('0 requests');
  });

  it('shows the totals of the period', async () => {
    renderPage();
    const totals = await screen.findByTestId('usage-totals');
    expect(totals).toHaveTextContent('Requests420');
    expect(totals).toHaveTextContent('Prompt tokens1,100,000');
    expect(totals).toHaveTextContent('Completion tokens130,000');
  });

  it('offers model and node only for the 7-day range, and the group-by follows the range', async () => {
    const { fetchUsage } = renderPage();
    await table('Usage by day');
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Group by' }));
    expect(screen.getByRole('option', { name: 'Model' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('option', { name: 'Node' })).toHaveAttribute('aria-disabled', 'true');
    fireEvent.keyDown(screen.getByRole('listbox'), { key: 'Escape' });
    expect(screen.getByText('Model and node views cover 7 days.')).toBeInTheDocument();

    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Range' }));
    fireEvent.click(await screen.findByRole('option', { name: 'Last 7 days' }));
    await waitFor(() => expect(fetchUsage).toHaveBeenLastCalledWith({ days: 7, groupBy: 'day', keyId: null, all: false }, USAGE_NOW));
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Group by' }));
    fireEvent.click(await screen.findByRole('option', { name: 'Model' }));
    await waitFor(() => expect(fetchUsage).toHaveBeenLastCalledWith({ days: 7, groupBy: 'model', keyId: null, all: false }, USAGE_NOW));
    const rows = within(await table('Usage by model')).getAllByRole('row').slice(1);
    expect(rows.map((row) => within(row).getAllByRole('cell')[0]!.textContent)).toEqual(['qwen3-coder-next-q4km', 'qwen3.5-9b-q4km', '(none)']);

    // Going back to 30 days returns to the day view.
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Range' }));
    fireEvent.click(await screen.findByRole('option', { name: 'Last 30 days' }));
    await waitFor(() => expect(fetchUsage).toHaveBeenLastCalledWith({ days: 30, groupBy: 'day', keyId: null, all: false }, USAGE_NOW));
  });

  it('can narrow to one of your keys', async () => {
    const { fetchUsage } = renderPage();
    await table('Usage by day');
    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Key' }));
    fireEvent.click(await screen.findByRole('option', { name: /OpenCode laptop \(lai_Ab3dEf…\)/ }));
    await waitFor(() => expect(fetchUsage).toHaveBeenLastCalledWith({ days: 30, groupBy: 'day', keyId: '00000000-0000-4000-8000-000000000001', all: false }, USAGE_NOW));
  });

  it('has the every-user switch for a gateway administrator only', async () => {
    renderPage();
    await table('Usage by day');
    expect(screen.queryByLabelText("Show every user's keys")).not.toBeInTheDocument();
  });

  it('lets a gateway administrator read every key', async () => {
    permissions = ['gateway:use', 'gateway:admin'];
    const { fetchUsage } = renderPage();
    await table('Usage by day');
    fireEvent.click(screen.getByLabelText("Show every user's keys"));
    await waitFor(() => expect(fetchUsage).toHaveBeenLastCalledWith({ days: 30, groupBy: 'day', keyId: null, all: true }, USAGE_NOW));
  });

  it('says there is nothing yet instead of a table of zeros', async () => {
    renderPage({ fetchUsage: vi.fn(async () => emptyUsage()) });
    expect(await screen.findByTestId('usage-empty')).toHaveTextContent('No usage in this period');
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('says it could not load, and offers to try again', async () => {
    const fetchUsage = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue(usageByDay());
    renderPage({ fetchUsage });
    expect(await screen.findByText('Could not load the usage.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await table('Usage by day')).toBeInTheDocument();
  });

  it('links to the API keys page and marks Usage as the current page', async () => {
    renderPage();
    await table('Usage by day');
    const nav = screen.getByRole('navigation', { name: 'Gateway pages' });
    expect(within(nav).getByRole('link', { name: 'API keys' })).toHaveAttribute('href', '/gateway/keys');
    expect(within(nav).getByRole('link', { name: 'Usage' })).toHaveAttribute('aria-current', 'page');
  });

  it('shows no prompt, key or address anywhere', async () => {
    const { container } = renderPage();
    await table('Usage by day');
    expect(container.textContent).not.toMatch(/lai_[A-Za-z0-9_-]{43}/);
    expect(container.textContent).not.toMatch(/\b(?:\d{1,3}\.){3}\d{1,3}\b/);
  });

  describe('accessibility', () => {
    it('has no axe violations with rows, grouped by model, and when empty', async () => {
      const { unmount } = renderPage();
      await table('Usage by day');
      expect(await axeRun()).toEqual([]);
      unmount();

      const second = renderPage({ fetchUsage: vi.fn(async () => usageByModel()) });
      await table('Usage by model');
      expect(await axeRun()).toEqual([]);
      second.unmount();

      renderPage({ fetchUsage: vi.fn(async () => emptyUsage()) });
      await screen.findByTestId('usage-empty');
      expect(await axeRun()).toEqual([]);
    });
  });
});
