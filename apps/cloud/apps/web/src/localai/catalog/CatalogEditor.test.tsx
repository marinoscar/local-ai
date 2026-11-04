// The editor: it loads the base files, saves a NEW draft, follows that draft's lint (polled every 2 s until a verdict),
// jumps from a lint problem to its file and line, and enables Approve only for a saved, valid, unedited draft.
import { act, fireEvent, render, screen, within } from '@testing-library/react';
import axe from 'axe-core';

import { ApiError } from '@marinoscar/platform-web/core';

import type { CatalogApi, CatalogVersionSummary } from '../api/catalog';
import { CatalogEditor, type EditorSource } from './CatalogEditor';
import { BAD_MODELS_YAML, CAT_NOW, GOOD_REVISION, MODELS_YAML, PROBLEM, fakeCatalog, summary, wireframeVersions } from './fixture';

const flush = async (ms = 0) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

interface Setup {
  source?: EditorSource;
  versions?: CatalogVersionSummary[];
  fake?: ReturnType<typeof fakeCatalog>;
}

async function mount({ source = { kind: 'new', baseVersion: '2026.11.02-1' }, versions = wireframeVersions(), fake = fakeCatalog() }: Setup = {}) {
  const props = { onSaved: vi.fn(), onApprove: vi.fn(), onClose: vi.fn(), onDirtyChange: vi.fn() };
  const view = render(<CatalogEditor client={fake.api} source={source} versions={versions} today={CAT_NOW} {...props} />);
  await flush();
  return { fake, props, ...view };
}

const area = () => screen.getByRole('textbox', { name: /^Contents of / }) as HTMLTextAreaElement;
const setText = (value: string) => fireEvent.change(area(), { target: { value } });
const selectedText = () => area().value.slice(area().selectionStart, area().selectionEnd);

describe('CatalogEditor', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('starts a new draft from the approved version: its files in the tree, models.yaml open, a greater version suggested', async () => {
    const { fake } = await mount();
    expect(screen.getByRole('region', { name: 'New draft from 2026.11.02-1' })).toBeInTheDocument();
    const tree = screen.getByRole('list', { name: 'Catalog files' });
    expect(within(tree).getAllByRole('button').filter((button) => !/Remove/.test(button.getAttribute('aria-label') ?? '')).map((button) => button.textContent)).toEqual([
      'CATALOG_VERSION',
      'models.yaml',
      'ark.yaml',
      'profiles/small-8gb.yaml',
    ]);
    expect(area()).toHaveAccessibleName('Contents of models.yaml');
    expect(area().value).toBe(MODELS_YAML);
    expect(screen.getByLabelText('Version')).toHaveValue('2026.11.03-2');
    expect(fake.calls.get).toHaveBeenCalledWith('2026.11.02-1');
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    expect(screen.getByText(/Save the draft first/)).toBeInTheDocument();
  });

  it('shows CATALOG_VERSION read only, from the Version field', async () => {
    await mount();
    fireEvent.click(screen.getByRole('button', { name: 'CATALOG_VERSION' }));
    expect(area()).toHaveAttribute('readonly');
    expect(area().value).toBe('2026.11.03-2\n');
    fireEvent.change(screen.getByLabelText('Version'), { target: { value: '2026.11.09-1' } });
    expect(area().value).toBe('2026.11.09-1\n');
  });

  it('saves a NEW draft with the edited files, then follows its lint until the verdict arrives', async () => {
    const fake = fakeCatalog(wireframeVersions(), { lintProblems: () => [PROBLEM] });
    const { props } = await mount({ fake });
    setText(BAD_MODELS_YAML);
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(true);
    fireEvent.change(screen.getByLabelText('Notes'), { target: { value: 'try a branch' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save draft' }));
    await flush();

    const body = fake.calls.createDraft.mock.calls[0]![0];
    expect(body).toMatchObject({ version: '2026.11.03-2', baseVersion: '2026.11.02-1', notes: 'try a branch', source: 'editor' });
    expect(body.files.map((file: { path: string }) => file.path)).toEqual(['models.yaml', 'ark.yaml', 'profiles/small-8gb.yaml']);
    expect(body.files[0].content).toBe(BAD_MODELS_YAML);
    expect(props.onSaved).toHaveBeenCalledWith('2026.11.03-2');
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(false);

    // The lint has no verdict yet: the panel says so, and the version is polled every 2 s.
    expect(screen.getByRole('status')).toHaveTextContent('Checking the catalog…');
    const getsBefore = fake.calls.get.mock.calls.filter(([version]) => version === '2026.11.03-2').length;
    await flush(1999);
    expect(fake.calls.get.mock.calls.filter(([version]) => version === '2026.11.03-2')).toHaveLength(getsBefore);
    await flush(1);
    expect(fake.calls.get.mock.calls.filter(([version]) => version === '2026.11.03-2')).toHaveLength(getsBefore + 1);

    // The verdict lands: the panel updates by itself, and polling stops.
    fake.settle('2026.11.03-2');
    await flush(2000);
    const label = 'LAI-CAT-011 models.yaml:models[0].source.revision must be a 40-hex commit';
    expect(screen.getByRole('button', { name: label })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    const settledGets = fake.calls.get.mock.calls.length;
    await flush(10000);
    expect(fake.calls.get).toHaveBeenCalledTimes(settledGets);
    // The version field moved on, so another save is possible.
    expect(screen.getByLabelText('Version')).toHaveValue('2026.11.03-3');
  });

  it('jumps from a lint problem to its file and line', async () => {
    const versions = [summary('2026.11.03-1', 'invalid', { source: 'editor', problemCount: 2 }), ...wireframeVersions().slice(1)];
    const { fake } = await mount({ source: { kind: 'edit', version: '2026.11.03-1' }, versions });
    // Another file is open: the click must switch to models.yaml.
    fireEvent.click(screen.getByRole('button', { name: 'ark.yaml' }));
    expect(area()).toHaveAccessibleName('Contents of ark.yaml');
    expect(fake.calls.get).toHaveBeenCalledWith('2026.11.03-1');
    const second = screen.getByRole('button', { name: 'LAI-CAT-011 models.yaml:models[1].source.revision must be a 40-hex commit' });
    fireEvent.click(second);
    await flush();
    expect(area()).toHaveAccessibleName('Contents of models.yaml');
    expect(selectedText()).toBe(`      revision: ${GOOD_REVISION}`);
    expect(area().value.slice(0, area().selectionStart).split('\n')).toHaveLength(8); // line 8 of the file
    expect(document.activeElement).toBe(area());
    fireEvent.click(screen.getByRole('button', { name: 'LAI-CAT-011 models.yaml:models[0].source.revision must be a 40-hex commit' }));
    await flush();
    expect(area().value.slice(0, area().selectionStart).split('\n')).toHaveLength(5);
    expect(selectedText()).toBe('      revision: main');
  });

  it('marks files with problems in the tree and warns when the text changed since the verdict', async () => {
    const versions = [summary('2026.11.03-1', 'invalid', { source: 'editor', problemCount: 2 }), ...wireframeVersions().slice(1)];
    await mount({ source: { kind: 'edit', version: '2026.11.03-1' }, versions });
    expect(screen.getByLabelText('2 lint problems')).toBeInTheDocument();
    expect(screen.queryByText(/You changed the text after/)).toBeNull();
    setText('schema_version: 1\n');
    expect(screen.getByText(/You changed the text after 2026.11.03-1 was saved/)).toBeInTheDocument();
  });

  it('enables Approve only for a saved, valid draft with no edits since, and approves that version', async () => {
    const fake = fakeCatalog(wireframeVersions(), { autoLint: true });
    const { props } = await mount({ fake });
    const approve = () => screen.getByRole('button', { name: 'Approve' });
    expect(approve()).toBeDisabled();
    setText(`${MODELS_YAML}# note\n`);
    fireEvent.click(screen.getByRole('button', { name: 'Save draft' }));
    await flush();
    await flush(0);
    expect(screen.getByText('No problems found.')).toBeInTheDocument();
    expect(approve()).toBeEnabled();
    // An edit after the save turns it off again until the text is saved.
    setText(`${MODELS_YAML}# another\n`);
    expect(approve()).toBeDisabled();
    expect(screen.getByText(/Save the draft again to check it/)).toBeInTheDocument();
    setText(`${MODELS_YAML}# note\n`);
    expect(approve()).toBeEnabled();
    fireEvent.click(approve());
    expect(props.onApprove).toHaveBeenCalledWith('2026.11.03-2');
  });

  it('refuses a version that is not greater than every existing one, in words, and does not save it', async () => {
    const { fake } = await mount();
    const field = screen.getByLabelText('Version');
    fireEvent.change(field, { target: { value: '2026.11.02-12' } });
    expect(screen.getByText('Must be greater than 2026.11.03-1, the newest existing version.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save draft' })).toBeDisabled();
    fireEvent.change(field, { target: { value: '2026.11.3-1' } });
    expect(screen.getByText('Use YYYY.MM.DD-N, for example 2026.11.03-1.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save draft' })).toBeDisabled();
    fireEvent.change(field, { target: { value: '2026.11.03-2' } });
    expect(screen.getByRole('button', { name: 'Save draft' })).toBeEnabled();
    expect(fake.calls.createDraft).not.toHaveBeenCalled();
  });

  it('refuses a catalog over 2 MB', async () => {
    await mount();
    setText('x'.repeat(2 * 1024 * 1024 + 1));
    expect(screen.getByRole('alert')).toHaveTextContent('The catalog is 2.00 MB; the limit is 2 MB.');
    expect(screen.getByRole('button', { name: 'Save draft' })).toBeDisabled();
    setText('x');
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('adds and removes profiles by the path rule', async () => {
    await mount();
    const name = screen.getByLabelText('New profile');
    const add = screen.getByRole('button', { name: 'Add profile' });
    fireEvent.change(name, { target: { value: 'Not Valid' } });
    fireEvent.click(add);
    expect(screen.getByText('Use lower-case letters, digits and dashes, for example small-8gb.')).toBeInTheDocument();
    fireEvent.change(name, { target: { value: 'small-8gb' } });
    fireEvent.click(add);
    expect(screen.getByText('profiles/small-8gb.yaml already exists.')).toBeInTheDocument();
    fireEvent.change(name, { target: { value: 'cuda-12gb' } });
    fireEvent.click(add);
    expect(area()).toHaveAccessibleName('Contents of profiles/cuda-12gb.yaml');
    expect(area().value).toBe('');
    fireEvent.click(screen.getByRole('button', { name: 'Remove profiles/cuda-12gb.yaml' }));
    expect(screen.queryByRole('button', { name: 'profiles/cuda-12gb.yaml' })).toBeNull();
    // Only profiles can be removed.
    expect(screen.queryByRole('button', { name: 'Remove models.yaml' })).toBeNull();
  });

  it('shows the server refusal when saving fails and keeps the text', async () => {
    const fake = fakeCatalog();
    fake.calls.createDraft.mockRejectedValueOnce(new ApiError('Version 2026.11.03-2 must be greater than every existing version.', 400, undefined, { reason: 'version_not_greater' }));
    await mount({ fake });
    setText('changed\n');
    fireEvent.click(screen.getByRole('button', { name: 'Save draft' }));
    await flush();
    expect(screen.getByText('Version 2026.11.03-2 must be greater than every existing version.')).toBeInTheDocument();
    expect(area().value).toBe('changed\n');
  });

  it('starts empty with no base version, and says a failed load', async () => {
    const empty = fakeCatalog([]);
    await mount({ source: { kind: 'new', baseVersion: null }, versions: [], fake: empty });
    expect(screen.getByRole('region', { name: 'New draft' })).toBeInTheDocument();
    expect(area().value).toBe('');
    expect(screen.getByLabelText('Version')).toHaveValue('2026.11.03-1');
  });

  it('says so when the base cannot be loaded', async () => {
    const broken = fakeCatalog();
    broken.calls.get.mockRejectedValue(new Error('network'));
    const { props } = await mount({ fake: broken });
    expect(screen.getByRole('alert')).toHaveTextContent('Could not load 2026.11.02-1 for editing.');
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(props.onClose).toHaveBeenCalled();
  });

  it('reports no unsaved edits when it goes away', async () => {
    const { props, unmount } = await mount();
    setText('x');
    unmount();
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it('has no axe violations', async () => {
    vi.useRealTimers();
    const fake = fakeCatalog();
    const { container } = render(<CatalogEditor client={fake.api as CatalogApi} source={{ kind: 'new', baseVersion: '2026.11.02-1' }} versions={wireframeVersions()} today={CAT_NOW} onSaved={() => undefined} onApprove={() => undefined} onClose={() => undefined} />);
    await screen.findByRole('textbox', { name: 'Contents of models.yaml' });
    expect((await axe.run(container)).violations).toEqual([]);
    expect(GOOD_REVISION).toHaveLength(40);
  });
});
