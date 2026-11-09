// The Models page: the wireframe's rows, read-only for a user and editable for a gateway administrator, the policy dialog
// (validation, the API's refusals, the PUT body), the narrow layout, and the axe checks. Transports and permissions are stubbed.
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';
import { MemoryRouter } from 'react-router-dom';

import { modelsFixture } from './modelsFixture';
import { ModelsPage, type ModelsPageProps } from './ModelsPage';
import type { ModelRow, PolicyInput } from './modelsTypes';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

function renderPage(overrides: ModelsPageProps = {}) {
  const listModels = (overrides.listModels ?? vi.fn(async (): Promise<ModelRow[]> => modelsFixture())) as ReturnType<typeof vi.fn> & (() => Promise<ModelRow[]>);
  const saveModelPolicy = overrides.saveModelPolicy ?? vi.fn(async (_id: string, _input: PolicyInput) => modelsFixture()[0]);
  const view = render(
    <MemoryRouter initialEntries={['/gateway/models']}>
      <ModelsPage listModels={listModels} saveModelPolicy={saveModelPolicy} />
    </MemoryRouter>,
  );
  return { ...view, listModels, saveModelPolicy };
}

const table = () => screen.findByRole('table', { name: 'Models' });
const axeViolations = async () => (await runAxe(document.body)).violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html).join(' | ')}`);

function stubNarrowScreen(narrow: boolean): void {
  window.matchMedia = ((query: string) => ({
    matches: narrow && /max-width/.test(query),
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

describe('ModelsPage', () => {
  beforeEach(() => {
    permissions = ['gateway:use'];
    // @ts-expect-error jsdom has no matchMedia: MUI's useMediaQuery falls back to the wide layout
    delete window.matchMedia;
  });

  it('renders the wireframe: model, state, nodes, offered, aliases, on-demand and preferred nodes', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: 'Models' })).toBeInTheDocument();
    const rows = within(await table()).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(4);
    expect(rows[0]).toHaveTextContent('Qwen3 Coder Next');
    expect(rows[0]).toHaveTextContent('qwen3-coder-next-q4km');
    expect(rows[0]).toHaveTextContent('loaded');
    expect(rows[0]).toHaveTextContent('ai-n1');
    expect(rows[0]).toHaveTextContent('enabled');
    expect(rows[0]).toHaveTextContent('lab-coder');
    expect(rows[0]).toHaveTextContent('off');
    expect(rows[1]).toHaveTextContent('loadable');
    expect(rows[1]).toHaveTextContent('on · wait 120 s');
    expect(rows[1]).toHaveTextContent('ai-n2');
    expect(rows[2]).toHaveTextContent('disabled');
    expect(rows[3]).toHaveTextContent('no node');
    const nav = screen.getByRole('navigation', { name: 'Gateway pages' });
    expect(within(nav).getByRole('link', { name: 'API keys' })).toHaveAttribute('href', '/gateway/keys');
    expect(within(nav).getByRole('link', { name: 'Models' })).toHaveAttribute('aria-current', 'page');
  });

  it('a user without gateway:admin reads only: no Edit button', async () => {
    renderPage();
    await table();
    expect(screen.queryByRole('button', { name: /^Edit/ })).not.toBeInTheDocument();
  });

  it('an administrator can edit every row', async () => {
    permissions = ['gateway:use', 'gateway:admin'];
    renderPage();
    await table();
    expect(screen.getAllByRole('button', { name: /^Edit the policy of / })).toHaveLength(4);
  });

  it('says it could not load the models, and offers to try again', async () => {
    const listModels = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue(modelsFixture());
    renderPage({ listModels });
    expect(await screen.findByText('Could not load the models.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await table();
  });

  it('has a friendly empty state', async () => {
    renderPage({ listModels: vi.fn(async () => []) });
    expect(await screen.findByTestId('models-empty')).toHaveTextContent('No models to show');
  });

  describe('the policy dialog', () => {
    beforeEach(() => {
      permissions = ['gateway:use', 'gateway:admin'];
    });

    async function openFor(name: string) {
      const view = renderPage();
      await table();
      fireEvent.click(screen.getByRole('button', { name: `Edit the policy of ${name}` }));
      return { ...view, dialog: await screen.findByRole('dialog', { name: `Policy for ${name}` }) };
    }

    it('starts from the stored policy', async () => {
      const { dialog } = await openFor('Qwen 3.6 35B A3B');
      expect(within(dialog).getByLabelText(/Load on demand/)).toBeChecked();
      expect(within(dialog).getByLabelText(/Wait for a load/)).toHaveValue(120);
      expect(within(dialog).getByLabelText(/Preferred nodes/)).toHaveValue('ai-n2');
    });

    it('saves the edited policy as the PUT body and refreshes', async () => {
      const { dialog, saveModelPolicy, listModels } = await openFor('Qwen3 Coder Next');
      fireEvent.change(within(dialog).getByLabelText('Aliases'), { target: { value: 'lab-coder, coder-two' } });
      fireEvent.click(within(dialog).getByLabelText(/Load on demand/));
      fireEvent.change(within(dialog).getByLabelText(/Wait for a load/), { target: { value: '45' } });
      fireEvent.change(within(dialog).getByLabelText(/Preferred nodes/), { target: { value: 'ai-n1, ai-n2' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
      await waitFor(() => expect(saveModelPolicy).toHaveBeenCalledTimes(1));
      expect(saveModelPolicy).toHaveBeenCalledWith('qwen3-coder-next-q4km', {
        enabled: true,
        aliases: ['lab-coder', 'coder-two'],
        onDemandLoad: true,
        loadWaitS: 45,
        firstByteTimeoutS: null,
        preferredNodes: ['ai-n1', 'ai-n2'],
      });
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
      expect(screen.getByText(/Saved the policy of Qwen3 Coder Next/)).toBeInTheDocument();
      expect(listModels.mock.calls.length).toBeGreaterThan(1);
    });

    it('refuses an alias that is a model id before sending anything', async () => {
      const { dialog, saveModelPolicy } = await openFor('Qwen3 Coder Next');
      fireEvent.change(within(dialog).getByLabelText('Aliases'), { target: { value: 'whisper-small' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
      expect(await within(dialog).findByText(/already the id of a model/)).toBeInTheDocument();
      expect(saveModelPolicy).not.toHaveBeenCalled();
    });

    it("shows the API's refusal and stays open (an alias another model owns)", async () => {
      const saveModelPolicy = vi.fn().mockRejectedValue(new Error('The alias lab-coder already belongs to qwen3.5-4b-q4km'));
      const view = renderPage({ saveModelPolicy });
      await table();
      fireEvent.click(screen.getByRole('button', { name: 'Edit the policy of Whisper Small' }));
      const dialog = await screen.findByRole('dialog', { name: 'Policy for Whisper Small' });
      fireEvent.change(within(dialog).getByLabelText('Aliases'), { target: { value: 'lab-coder' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
      expect(await within(dialog).findByRole('alert')).toHaveTextContent('already belongs to');
      expect(screen.getByRole('dialog')).toBeInTheDocument();
      view.unmount();
    });

    it('Cancel closes without saving', async () => {
      const { dialog, saveModelPolicy } = await openFor('Qwen3 Coder Next');
      fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
      expect(saveModelPolicy).not.toHaveBeenCalled();
    });
  });

  describe('a narrow screen', () => {
    afterEach(() => stubNarrowScreen(false));

    it('lists the models as cards, with the same facts and the Edit button', async () => {
      permissions = ['gateway:use', 'gateway:admin'];
      stubNarrowScreen(true);
      renderPage();
      const list = await screen.findByRole('list', { name: 'Models' });
      const items = within(list).getAllByRole('listitem');
      expect(items).toHaveLength(4);
      expect(items[1]).toHaveTextContent('Qwen 3.6 35B A3B');
      expect(items[1]).toHaveTextContent('on · wait 120 s');
      expect(within(items[1]!).getByRole('button', { name: /^Edit the policy of/ })).toBeInTheDocument();
      expect(screen.queryByRole('table')).not.toBeInTheDocument();
    });
  });

  describe('accessibility', () => {
    it('has no axe violations in the table, the narrow list, the dialog and when empty', async () => {
      permissions = ['gateway:use', 'gateway:admin'];
      const wide = renderPage();
      await table();
      expect(await axeViolations()).toEqual([]);
      fireEvent.click(screen.getByRole('button', { name: 'Edit the policy of Qwen3 Coder Next' }));
      await screen.findByRole('dialog');
      expect(await axeViolations()).toEqual([]);
      wide.unmount();

      stubNarrowScreen(true);
      const narrow = renderPage();
      await screen.findByRole('list', { name: 'Models' });
      expect(await axeViolations()).toEqual([]);
      narrow.unmount();
      stubNarrowScreen(false);

      renderPage({ listModels: vi.fn(async () => []) });
      await screen.findByTestId('models-empty');
      expect(await axeViolations()).toEqual([]);
    });
  });
});
