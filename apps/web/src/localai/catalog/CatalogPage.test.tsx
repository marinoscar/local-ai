// The Catalog page over the wireframe's three versions and the 3-node lab: header, table, permissions (a viewer sees
// no write control), the diff, import, approve, push to nodes, and the empty, loading and failure states.
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import axe from 'axe-core';
import { MemoryRouter } from 'react-router-dom';

import { ApiError } from '@marinoscar/platform-web/core';

import { labFixture } from '../lab/fixture';
import { approvedHeadline, CatalogPage } from './CatalogPage';
import { CAT_NOW, fakeCatalog, summary, wireframeVersions } from './fixture';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

type Fake = ReturnType<typeof fakeCatalog>;

function renderPage(fake: Fake = fakeCatalog(), fetchNodes: () => Promise<ReturnType<typeof labFixture>> = async () => labFixture()) {
  return render(
    <MemoryRouter>
      <CatalogPage client={fake.api} fetchNodes={fetchNodes} today={CAT_NOW} />
    </MemoryRouter>,
  );
}

const row = (version: string) => within(screen.getByTestId(`row-${version}`));
const ready = async () => {
  await screen.findByTestId('row-2026.11.02-1');
  await screen.findByText(/2 of 3 nodes on it/);
};

describe('CatalogPage', () => {
  beforeEach(() => {
    permissions = ['lab:read', 'lab:admin'];
    vi.spyOn(window, 'confirm').mockReturnValue(true);
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('renders the wireframe: the approved headline and the three versions with their lint and actions', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: 'Catalog' })).toBeInTheDocument();
    await ready();
    expect(screen.getByTestId('catalog-headline')).toHaveTextContent('Approved: 2026.11.02-1 (catalog-2026.11.02-1) · approved by Person A · 2 of 3 nodes on it');

    const rows = within(screen.getByRole('table', { name: 'Catalog versions' })).getAllByRole('row').slice(1);
    expect(rows.map((item) => item.getAttribute('data-testid'))).toEqual(['row-2026.11.03-1', 'row-2026.11.02-1', 'row-2026.10.28-2']);

    expect(row('2026.11.03-1').getByText('editor')).toBeInTheDocument();
    expect(row('2026.11.03-1').getByTestId('status-invalid')).toHaveTextContent('invalid');
    expect(row('2026.11.03-1').getByTestId('lint-2026.11.03-1')).toHaveTextContent('✖ 2 errors');
    expect(row('2026.11.03-1').getAllByRole('button').map((button) => button.textContent)).toEqual(['Edit', 'Lint', 'Diff vs approved']);

    expect(row('2026.11.02-1').getByTestId('status-approved')).toHaveTextContent('approved');
    expect(row('2026.11.02-1').getByTestId('lint-2026.11.02-1')).toHaveTextContent('No problems');
    expect(row('2026.11.02-1').getAllByRole('button').map((button) => button.textContent)).toEqual(['Diff', 'Push to nodes']);

    expect(row('2026.10.28-2').getByTestId('status-superseded')).toHaveTextContent('superseded');
    expect(row('2026.10.28-2').getAllByRole('button').map((button) => button.textContent)).toEqual(['Diff vs approved']);
    expect(screen.getByRole('button', { name: 'Import release…' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New draft' })).toBeInTheDocument();
  });

  it('shows a viewer the table and the Diff buttons and nothing that writes', async () => {
    permissions = ['lab:read'];
    renderPage();
    await ready();
    for (const name of [/import release/i, /new draft/i, /^edit/i, /^lint/i, /push/i, /^approve$/i, /save/i]) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
    expect(screen.getAllByRole('button').map((button) => button.textContent)).toEqual(['Diff vs approved', 'Diff', 'Diff vs approved']);
  });

  it('says the approver is unknown without inventing a name, and that nothing is approved yet', () => {
    expect(approvedHeadline(summary('2026.11.02-1', 'approved', { approvedByName: null, sourceRef: null }), { on: 1, total: 1 })).toBe('Approved: 2026.11.02-1 · approved by an administrator · 1 of 1 node on it');
    expect(approvedHeadline(summary('2026.11.02-1', 'approved'), null)).toBe('Approved: 2026.11.02-1 (catalog-2026.11.02-1) · approved by Person A');
    expect(approvedHeadline(undefined, null)).toBe('Nothing is approved yet. Enrolled nodes keep the catalog they already have.');
  });

  it('opens a diff against the approved version, closes it, and compares the approved row with the version before it', async () => {
    const fake = fakeCatalog();
    renderPage(fake);
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'Diff 2026.11.03-1 against approved' }));
    const panel = await screen.findByRole('region', { name: 'Diff of 2026.11.03-1 against 2026.11.02-1' });
    await within(panel).findByTestId('diff-view');
    expect(within(panel).getByTestId('diff-summary')).toHaveTextContent('2 files changed, +2 -2 lines');
    fireEvent.click(within(panel).getByRole('button', { name: 'Close diff' }));
    expect(screen.queryByTestId('diff-panel')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Diff 2026.11.02-1 against the previous version' }));
    const previous = await screen.findByRole('region', { name: 'Diff of 2026.11.02-1 against 2026.10.28-2' });
    await within(previous).findByTestId('diff-view');
    expect(fake.calls.get).toHaveBeenCalledWith('2026.10.28-2');
  });

  it('says there is nothing to compare with when nothing is approved', async () => {
    const fake = fakeCatalog([summary('2026.11.03-1', 'valid', { source: 'editor' })]);
    renderPage(fake);
    fireEvent.click(await screen.findByRole('button', { name: 'Diff 2026.11.03-1 against approved' }));
    expect(await screen.findByText('No catalog is approved yet, so there is nothing to compare with.')).toBeInTheDocument();
  });

  it('imports a release: the dialog queues it, the page says the draft will appear, and a 409 is "This version already exists."', async () => {
    const fake = fakeCatalog();
    renderPage(fake);
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'Import release…' }));
    fireEvent.change(screen.getByLabelText('Release tag'), { target: { value: 'catalog-2026.11.02-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Import' }));
    expect(await screen.findByText('This version already exists.')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Release tag'), { target: { value: 'catalog-2026.11.04-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Import' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(screen.getByRole('status')).toHaveTextContent('Importing catalog-2026.11.04-1. The draft appears in the table when the download finishes.');
    expect(fake.calls.importRelease).toHaveBeenLastCalledWith('catalog-2026.11.04-1');
  });

  it('queues a lint from the row and shows "checking…" until the verdict changes', async () => {
    const fake = fakeCatalog();
    renderPage(fake);
    await ready();
    expect(row('2026.11.03-1').getByRole('button', { name: 'Lint 2026.11.03-1' })).toBeEnabled();
    fireEvent.click(row('2026.11.03-1').getByRole('button', { name: 'Lint 2026.11.03-1' }));
    await waitFor(() => expect(fake.calls.lint).toHaveBeenCalledWith('2026.11.03-1'));
    expect(await within(screen.getByTestId('row-2026.11.03-1')).findByText('checking…')).toBeInTheDocument();
    expect(row('2026.11.03-1').getByRole('button', { name: 'Lint 2026.11.03-1' })).toBeDisabled();
    // The job lands: a new lintedAt ends the wait.
    fake.rows.get('2026.11.03-1')!.lintedAt = '2026-11-03T12:30:00.000Z';
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 2100));
    });
    await waitFor(() => expect(row('2026.11.03-1').getByTestId('lint-2026.11.03-1')).toHaveTextContent('✖ 2 errors'));
  }, 10000);

  it('says why a lint could not be queued', async () => {
    const fake = fakeCatalog();
    fake.calls.lint.mockRejectedValueOnce(new ApiError('Catalog 2026.11.03-1 is approved and cannot be linted again.', 409));
    renderPage(fake);
    await ready();
    fireEvent.click(row('2026.11.03-1').getByRole('button', { name: 'Lint 2026.11.03-1' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Catalog 2026.11.03-1 is approved and cannot be linted again.');
    expect(row('2026.11.03-1').getByRole('button', { name: 'Lint 2026.11.03-1' })).toBeEnabled();
  });

  it('shows "checker failed" for a version the checker could not run on', async () => {
    const failed = summary('2026.11.03-1', 'draft', { source: 'editor', lintError: { code: 'LAI-CLD-033', reason: 'aictl_missing', message: 'aictl is not installed' } });
    renderPage(fakeCatalog([failed, ...wireframeVersions().slice(1)]));
    await ready();
    expect(row('2026.11.03-1').getByTestId('lint-2026.11.03-1')).toHaveTextContent('✖ checker failed');
  });

  it('pushes the approved catalog to the healthy and degraded nodes only, and says how many', async () => {
    const fake = fakeCatalog();
    renderPage(fake);
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'Push 2026.11.02-1 to nodes' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Sent to 2 nodes.'));
    expect(fake.calls.refreshNode.mock.calls.map(([nodeId]) => nodeId)).toEqual(['ai-n1', 'ai-n2']);
  });

  it('counts a failed push and sends to a degraded node too', async () => {
    const nodes = labFixture();
    nodes.nodes[2]!.health = 'degraded';
    const fake = fakeCatalog();
    fake.calls.refreshNode.mockImplementation(async (nodeId: string) => {
      if (nodeId === 'ai-n2') throw new ApiError('Node is busy', 409);
    });
    renderPage(fake, async () => nodes);
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'Push 2026.11.02-1 to nodes' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Sent to 2 nodes, 1 failed.'));
    expect(fake.calls.refreshNode).toHaveBeenCalledTimes(3);
  });

  it('sends nothing when no node is healthy, and says so', async () => {
    const nodes = labFixture();
    nodes.nodes.forEach((node) => {
      node.health = 'down';
    });
    const fake = fakeCatalog();
    renderPage(fake, async () => nodes);
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'Push 2026.11.02-1 to nodes' }));
    expect(await screen.findByRole('status')).toHaveTextContent('No node is healthy right now, so nothing was sent.');
    expect(fake.calls.refreshNode).not.toHaveBeenCalled();
  });

  it('edits a draft, saves a new draft, approves it with "Push to nodes now" and shows the previous one superseded', async () => {
    const fake = fakeCatalog(wireframeVersions(), { autoLint: true });
    renderPage(fake);
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'New draft' }));
    const editor = await screen.findByTestId('catalog-editor');
    await within(editor).findByRole('textbox', { name: 'Contents of models.yaml' });
    fireEvent.click(within(editor).getByRole('button', { name: 'Save draft' }));
    await within(editor).findByText('No problems found.');
    await waitFor(() => expect(within(editor).getByRole('button', { name: 'Approve' })).toBeEnabled());

    fireEvent.click(within(editor).getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve 2026.11.03-2 for all nodes?' });
    await within(dialog).findByText('Changes since 2026.11.02-1');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(fake.calls.approve).toHaveBeenCalledWith('2026.11.03-2', true);
    expect(screen.getByText('Approved 2026.11.03-2. 2026.11.02-1 is superseded. Refresh queued on 2 nodes.')).toBeInTheDocument();
    await waitFor(() => expect(row('2026.11.02-1').getByTestId('status-superseded')).toBeInTheDocument());
    expect(row('2026.11.03-2').getByTestId('status-approved')).toBeInTheDocument();
  }, 15000);

  it('asks before an unsaved edit is thrown away, and keeps the editor when the answer is no', async () => {
    renderPage();
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'New draft' }));
    const textarea = await screen.findByRole('textbox', { name: 'Contents of models.yaml' });
    fireEvent.change(textarea, { target: { value: 'changed\n' } });
    vi.mocked(window.confirm).mockReturnValueOnce(false);
    fireEvent.click(screen.getByRole('button', { name: 'Close the editor' }));
    expect(window.confirm).toHaveBeenCalledWith('Discard your unsaved changes?');
    expect(screen.getByTestId('catalog-editor')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Close the editor' }));
    expect(screen.queryByTestId('catalog-editor')).toBeNull();
  });

  it('starts a draft from the approved version, or from nothing when there is no version', async () => {
    const fake = fakeCatalog([]);
    renderPage(fake);
    expect(await screen.findByTestId('catalog-empty')).toHaveTextContent('Import a release or start a new draft.');
    fireEvent.click(screen.getByRole('button', { name: 'New draft' }));
    expect(await screen.findByRole('region', { name: 'New draft' })).toBeInTheDocument();
  });

  it('shows the empty state to a viewer without any button', async () => {
    permissions = ['lab:read'];
    renderPage(fakeCatalog([]));
    expect(await screen.findByTestId('catalog-empty')).toHaveTextContent('An administrator can import the first one.');
    expect(screen.queryAllByRole('button')).toHaveLength(0);
  });

  it('shows a spinner while loading, and an error with Try again when the first load fails', async () => {
    const fake = fakeCatalog();
    let release!: (value: ReturnType<typeof wireframeVersions>) => void;
    fake.calls.list.mockImplementationOnce(() => new Promise((resolve) => (release = resolve)));
    const view = renderPage(fake);
    expect(screen.getByLabelText('Loading the catalog')).toBeInTheDocument();
    await act(async () => release(wireframeVersions()));
    expect(await screen.findByTestId('row-2026.11.02-1')).toBeInTheDocument();
    view.unmount();

    const broken = fakeCatalog();
    broken.calls.list.mockRejectedValueOnce(new ApiError('boom', 500));
    renderPage(broken);
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load the catalog.');
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByTestId('row-2026.11.02-1')).toBeInTheDocument();
  });

  it('keeps the page without the node counts when the lab cannot be read', async () => {
    renderPage(fakeCatalog(), async () => {
      throw new Error('down');
    });
    await screen.findByTestId('row-2026.11.02-1');
    expect(screen.getByTestId('catalog-headline')).toHaveTextContent('Approved: 2026.11.02-1 (catalog-2026.11.02-1) · approved by Person A');
    expect(screen.getByTestId('catalog-headline').textContent).not.toMatch(/nodes? on it/);
  });

  it('loads no network assets: nothing in the page points off this origin', async () => {
    const view = renderPage();
    await ready();
    const urls = [...view.container.querySelectorAll('[src],[href]')].map((node) => node.getAttribute('src') ?? node.getAttribute('href') ?? '');
    expect(urls.filter((url) => /^(https?:)?\/\//.test(url))).toEqual([]);
  });

  it('has no axe violations for an admin with the editor open, a viewer, and the empty state', async () => {
    const admin = renderPage();
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'New draft' }));
    await screen.findByRole('textbox', { name: 'Contents of models.yaml' });
    expect((await axe.run(admin.container)).violations).toEqual([]);
    admin.unmount();

    permissions = ['lab:read'];
    const viewer = renderPage();
    await ready();
    fireEvent.click(screen.getByRole('button', { name: 'Diff 2026.11.03-1 against approved' }));
    await screen.findByTestId('diff-view');
    expect((await axe.run(viewer.container)).violations).toEqual([]);
    viewer.unmount();

    const empty = renderPage(fakeCatalog([]));
    await screen.findByTestId('catalog-empty');
    expect((await axe.run(empty.container)).violations).toEqual([]);
  }, 15000);
});
