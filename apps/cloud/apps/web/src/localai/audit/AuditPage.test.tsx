// The audit page over the API fixture: the rows, the filters and how they reach the API, the URL state, paging, the
// empty/failure/backwards-range states, that nothing secret-shaped is on screen, and the axe checks.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';
import { MemoryRouter } from 'react-router-dom';

import { AuditPage, filtersFromParams, paramsFromFilters } from './AuditPage';
import { auditFixture, filterFixture } from './fixture';
import { dayEndIso, dayStartIso } from './format';
import type { AuditPageData } from './types';

function renderPage(options: { fetchAudit?: (query: string) => Promise<AuditPageData>; fetchFilters?: () => Promise<ReturnType<typeof filterFixture>>; url?: string } = {}) {
  const fetchAudit = vi.fn(options.fetchAudit ?? (async () => auditFixture()));
  const fetchFilters = vi.fn(options.fetchFilters ?? (async () => filterFixture()));
  const view = render(
    <MemoryRouter initialEntries={[options.url ?? '/lab/audit']}>
      <AuditPage fetchAudit={fetchAudit} fetchFilters={fetchFilters} timeZone="UTC" />
    </MemoryRouter>,
  );
  return { ...view, fetchAudit, fetchFilters };
}

const last = (fn: { mock: { calls: unknown[][] } }) => new URLSearchParams(String(fn.mock.calls.at(-1)![0]));

describe('AuditPage', () => {
  it('shows the title, the count and one row per event with time, actor, action, target and summary', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: 'Audit trail' })).toBeInTheDocument();
    expect(await screen.findByTestId('audit-count')).toHaveTextContent('123 events · page 1 of 3');
    const table = screen.getByRole('table', { name: 'Audit trail' });
    expect(within(table).getAllByRole('columnheader').map((header) => header.textContent)).toEqual(['Time', 'Actor', 'Action', 'Target', 'What happened']);
    const rows = within(table).getAllByTestId(/^audit-row-/);
    expect(rows).toHaveLength(10);

    const first = within(rows[0]!);
    expect(first.getByText('04 Nov 2026, 11:58:12')).toBeInTheDocument();
    expect(first.getByText('operator@example.test')).toBeInTheDocument();
    expect(first.getByText('Command finished')).toBeInTheDocument();
    expect(first.getByText('localai.command.settled')).toBeInTheDocument();
    expect(first.getByText('model.load on ai-n2 succeeded')).toBeInTheDocument();
  });

  it('labels a node actor as a node, and a system row as the system', async () => {
    renderPage();
    const mode = await screen.findByTestId('audit-row-cccccccc-cccc-4ccc-8ccc-000000000003');
    const actor = within(mode).getAllByRole('cell')[1]!;
    expect(actor).toHaveTextContent(/^node ai-n3$/);
    const system = within(screen.getByTestId('audit-row-cccccccc-cccc-4ccc-8ccc-000000000010')).getAllByRole('cell')[1]!;
    expect(system).toHaveTextContent(/^system$/);
  });

  it('links each row to its target: node, command, catalog, alert and gateway', async () => {
    renderPage();
    await screen.findByTestId('audit-count');
    const href = (name: string) => screen.getAllByRole('link', { name })[0]!.getAttribute('href');
    expect(href('Open node ai-n3')).toBe('/lab/nodes/ai-n3');
    expect(href('Open command 6b3c1c9e')).toBe('/lab/nodes/ai-n2?command=6b3c1c9e-7a52-4f4e-9d5e-0a1b2c3d4e5f');
    expect(href('Open catalog 2026.11.04-1')).toBe('/lab/catalog?version=2026.11.04-1');
    expect(href('Open alert 4a1e5c2d')).toBe('/lab/alerts');
    expect(href('Open gateway key gwk-1')).toBe('/gateway/keys');
  });

  it('renders an unknown action without a link, as its own name', async () => {
    renderPage();
    const row = await screen.findByTestId('audit-row-cccccccc-cccc-4ccc-8ccc-000000000010');
    expect(within(row).queryByRole('link')).toBeNull();
    expect(within(row).getAllByText('localai.future.thing').length).toBeGreaterThan(0);
    expect(within(row).getByText('future f-1')).toBeInTheDocument();
  });

  it('puts no token, key, bearer header or tailnet address on screen', async () => {
    const { container } = renderPage();
    await screen.findByTestId('audit-count');
    const text = container.textContent ?? '';
    expect(text).not.toMatch(/nod_[A-Za-z0-9_-]{20,}/);
    expect(text).not.toMatch(/\b[0-9a-f]{64}\b/);
    expect(text).not.toContain('Bearer');
    expect(text).not.toMatch(/\b100\.\d+\.\d+\.\d+\b/);
  });

  describe('filters', () => {
    it('start empty, and offer the nodes (retired marked) and the actions by group', async () => {
      renderPage();
      const node = (await screen.findByLabelText('Node')) as HTMLSelectElement;
      await waitFor(() => expect(within(node).getAllByRole('option').length).toBe(5));
      expect(within(node).getAllByRole('option').map((option) => option.textContent)).toEqual(['All nodes', 'ai-n1 (MS-S1 Max)', 'ai-n2 (MSI Stealth)', 'ai-n3 (HP ZBook)', 'ai-n4 (Old box) - retired']);
      const action = screen.getByLabelText('Action') as HTMLSelectElement;
      expect(within(action).getByRole('group', { name: 'Commands' })).toBeInTheDocument();
      expect(within(action).getByRole('option', { name: 'All commands actions' })).toHaveValue('localai.command.');
      expect(screen.getByLabelText('From')).toHaveValue('');
      expect(screen.getByText('Empty means the last 30 days')).toBeInTheDocument();
    });

    it('send node, action prefix and date range to the API together, as the API takes them', async () => {
      const { fetchAudit } = renderPage();
      const node = await screen.findByLabelText('Node');
      await waitFor(() => expect(within(node).getAllByRole('option').length).toBe(5));
      expect(fetchAudit).toHaveBeenLastCalledWith('');

      fireEvent.change(node, { target: { value: 'ai-n2' } });
      await waitFor(() => expect(last(fetchAudit).get('nodeId')).toBe('ai-n2'));
      fireEvent.change(screen.getByLabelText('Action'), { target: { value: 'localai.command.' } });
      fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-11-01' } });
      fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-11-02' } });
      await waitFor(() => expect(last(fetchAudit).get('to')).toBe(dayEndIso('2026-11-02')));
      const query = last(fetchAudit);
      expect(query.get('nodeId')).toBe('ai-n2');
      expect(query.get('action')).toBe('localai.command.');
      expect(query.get('from')).toBe(dayStartIso('2026-11-01'));
      expect(query.has('page')).toBe(false);
    });

    it('read the URL, so a shared link opens the same view', async () => {
      const { fetchAudit } = renderPage({ url: '/lab/audit?node=ai-n3&action=localai.node.mode_changed&from=2026-11-01&page=2' });
      await waitFor(() => expect(fetchAudit).toHaveBeenCalled());
      const query = last(fetchAudit);
      expect(query.get('nodeId')).toBe('ai-n3');
      expect(query.get('action')).toBe('localai.node.mode_changed');
      expect(query.get('from')).toBe(dayStartIso('2026-11-01'));
      expect(query.get('page')).toBe('2');
      expect(await screen.findByLabelText('Node')).toHaveValue('ai-n3');
    });

    it('keep a node or action from the URL that the options do not list', async () => {
      renderPage({ url: '/lab/audit?node=ai-n77&action=localai.future.' });
      expect(await screen.findByLabelText('Node')).toHaveValue('ai-n77');
      expect(screen.getByLabelText('Action')).toHaveValue('localai.future.');
    });

    it('clear with one button, and go back to page 1 whenever a filter changes', async () => {
      const { fetchAudit } = renderPage({ url: '/lab/audit?node=ai-n1&page=3' });
      await screen.findByTestId('audit-count');
      fireEvent.change(screen.getByLabelText('Node'), { target: { value: 'ai-n2' } });
      await waitFor(() => expect(last(fetchAudit).get('nodeId')).toBe('ai-n2'));
      expect(last(fetchAudit).has('page')).toBe(false);
      fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));
      await waitFor(() => expect(fetchAudit).toHaveBeenLastCalledWith(''));
      expect(screen.queryByRole('button', { name: 'Clear filters' })).toBeNull();
    });

    it('say so and read nothing when To is before From', async () => {
      const { fetchAudit } = renderPage({ url: '/lab/audit?from=2026-11-03&to=2026-11-01' });
      expect(await screen.findByText('To is before From')).toBeInTheDocument();
      expect(fetchAudit).not.toHaveBeenCalled();
      expect(screen.queryByTestId('audit-count')).toBeNull();
    });

    it('still read the trail when the filter choices cannot load', async () => {
      renderPage({
        fetchFilters: async () => {
          throw new Error('nope');
        },
      });
      expect(await screen.findByText('Could not load the filter choices. You can still read the trail.')).toBeInTheDocument();
      expect(await screen.findByTestId('audit-count')).toBeInTheDocument();
    });
  });

  describe('paging', () => {
    it('shows pages and asks the API for the one clicked', async () => {
      const { fetchAudit } = renderPage();
      await screen.findByTestId('audit-count');
      const nav = screen.getByRole('navigation');
      fireEvent.click(within(nav).getByRole('button', { name: /page 2/i }));
      await waitFor(() => expect(last(fetchAudit).get('page')).toBe('2'));
    });

    it('shows no pager for a single page', async () => {
      const single = auditFixture();
      single.total = 10;
      single.totalPages = 1;
      renderPage({ fetchAudit: async () => single });
      expect(await screen.findByTestId('audit-count')).toHaveTextContent('10 events · page 1 of 1');
      expect(screen.queryByRole('navigation')).toBeNull();
    });

    it('says "1 event" in the singular', async () => {
      const one = auditFixture();
      one.items = one.items.slice(0, 1);
      one.total = 1;
      one.totalPages = 1;
      renderPage({ fetchAudit: async () => one });
      expect(await screen.findByTestId('audit-count')).toHaveTextContent('1 event · page 1 of 1');
    });
  });

  describe('states', () => {
    it('says "No events match" when the filters match nothing', async () => {
      renderPage({ fetchAudit: async () => ({ items: [], page: 1, pageSize: 50, total: 0, totalPages: 1 }) });
      expect(await screen.findByTestId('audit-empty')).toHaveTextContent('No events match');
      expect(screen.queryByRole('table')).toBeNull();
    });

    it('shows a failure with a retry when nothing loaded', async () => {
      let fail = true;
      const { fetchAudit } = renderPage({
        fetchAudit: async () => {
          if (fail) throw new Error('boom');
          return auditFixture();
        },
      });
      expect(await screen.findByText('Could not load the audit trail.')).toBeInTheDocument();
      fail = false;
      fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
      expect(await screen.findByTestId('audit-count')).toBeInTheDocument();
      expect(fetchAudit).toHaveBeenCalledTimes(2);
    });
  });

  // axe walks the whole table; under a loaded CI machine that can pass the 5 s default, so these three get more room.
  const AXE_TIMEOUT = 30_000;
  const axeViolations = async (container: HTMLElement) => (await runAxe(container)).violations.map((violation) => `${violation.id}: ${violation.help}`);

  it('has no axe violations when loaded', async () => {
    const loaded = renderPage();
    await screen.findByTestId('audit-count');
    await waitFor(() => expect(within(screen.getByLabelText('Node', { selector: 'select' })).getAllByRole('option').length).toBe(5));
    expect(await axeViolations(loaded.container)).toEqual([]);
  }, AXE_TIMEOUT);

  it('has no axe violations when empty', async () => {
    const empty = renderPage({ fetchAudit: async () => ({ items: [], page: 1, pageSize: 50, total: 0, totalPages: 1 }) });
    await screen.findByTestId('audit-empty');
    expect(await axeViolations(empty.container)).toEqual([]);
  }, AXE_TIMEOUT);

  it('has no axe violations when failed', async () => {
    const failed = renderPage({
      fetchAudit: async () => {
        throw new Error('x');
      },
    });
    await screen.findByText('Could not load the audit trail.');
    expect(await axeViolations(failed.container)).toEqual([]);
  }, AXE_TIMEOUT);
});

describe('URL state', () => {
  it('round-trips the filters and drops anything malformed', () => {
    const filters = { nodeId: 'ai-n2', action: 'localai.command.', from: '2026-11-01', to: '2026-11-02', page: 4 };
    expect(filtersFromParams(paramsFromFilters(filters))).toEqual(filters);
    expect(filtersFromParams(new URLSearchParams('from=yesterday&to=11/02&page=-3'))).toEqual({ nodeId: '', action: '', from: '', to: '', page: 1 });
    expect(filtersFromParams(new URLSearchParams('page=2.5')).page).toBe(1);
    expect(paramsFromFilters({ nodeId: '', action: '', from: '', to: '', page: 1 }).toString()).toBe('');
  });
});

describe('the route', () => {
  it('is registered under /lab/audit and gated on lab:admin, the permission the API enforces', () => {
    const app = readFileSync(join(__dirname, '../../App.tsx'), 'utf8');
    expect(app).toMatch(/<Route path="lab\/audit" element=\{<Gate permission="lab:admin"><AuditPage \/><\/Gate>\} \/>/);
  });
});
