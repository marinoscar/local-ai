import { fireEvent, render, screen, within } from '@testing-library/react';
import axe from 'axe-core';

import { diffCatalogFiles, summarizeDiff } from './catalogDiff';
import { DiffPanel, DiffView } from './DiffView';

const file = (path: string, content: string) => ({ path, content, sha256: `sha-${content}` });
const base = [file('CATALOG_VERSION', '2026.11.02-1\n'), file('models.yaml', 'a\nb\nc\n'), file('ark.yaml', 'same\n')];
const target = [file('CATALOG_VERSION', '2026.11.03-1\n'), file('models.yaml', 'a\nB\nc\nd\n'), file('ark.yaml', 'same\n'), file('profiles/new.yaml', 'x\n')];
const files = diffCatalogFiles(base, target);
const summary = summarizeDiff(files);

describe('DiffView', () => {
  it('renders the summary and one section per changed file, with + and - on the lines', () => {
    render(<DiffView files={files} summary={summary} />);
    expect(screen.getByTestId('diff-summary')).toHaveTextContent('3 files changed, +4 -2 lines');
    const models = screen.getByRole('region', { name: 'Changes in models.yaml' });
    expect(within(models).getByText('changed · +2 -1')).toBeInTheDocument();
    const pre = models.querySelector('pre')!;
    expect(pre.textContent).toContain('@@ -1,3 +1,4 @@');
    expect(pre.textContent).toContain('-b');
    expect(pre.textContent).toContain('+B');
    expect(pre.textContent).toContain('+d');
    const kinds = [...pre.querySelectorAll('[data-kind]')].map((node) => `${node.getAttribute('data-kind')}:${node.textContent}`);
    expect(kinds).toEqual(['equal: a', 'remove:-b', 'add:+B', 'equal: c', 'add:+d']);
    expect(screen.getByRole('region', { name: 'Changes in profiles/new.yaml' })).toHaveTextContent('added · +1 -0');
  });

  it('lists unchanged files without expanding them', () => {
    render(<DiffView files={files} summary={summary} />);
    expect(screen.queryByRole('region', { name: 'Changes in ark.yaml' })).toBeNull();
    expect(screen.getByText('Unchanged: ark.yaml')).toBeInTheDocument();
  });

  it('says "No changes" for equal catalogs', () => {
    const same = diffCatalogFiles(base, base);
    render(<DiffView files={same} summary={summarizeDiff(same)} />);
    expect(screen.getByTestId('diff-summary')).toHaveTextContent('No changes');
  });

  it('has no axe violations', async () => {
    const { container } = render(<DiffView files={files} summary={summary} />);
    expect((await axe.run(container)).violations).toEqual([]);
  });
});

describe('DiffPanel', () => {
  const common = { title: 'Diff of 2026.11.03-1 against 2026.11.02-1', against: '2026.11.02-1', files, summary };

  it('shows a spinner, an error, an empty note or the diff, and closes', () => {
    const onClose = vi.fn();
    const { rerender } = render(<DiffPanel {...common} loading error={null} onClose={onClose} />);
    expect(screen.getByLabelText('Loading the diff')).toBeInTheDocument();
    rerender(<DiffPanel {...common} loading={false} error={new Error('x')} onClose={onClose} />);
    expect(screen.getByText('Could not load the diff. Close it and try again.')).toBeInTheDocument();
    rerender(<DiffPanel {...common} loading={false} error={null} against={null} emptyNote="No catalog is approved yet, so there is nothing to compare with." onClose={onClose} />);
    expect(screen.getByText(/nothing to compare with/)).toBeInTheDocument();
    expect(screen.queryByTestId('diff-view')).toBeNull();
    rerender(<DiffPanel {...common} loading={false} error={null} onClose={onClose} />);
    expect(screen.getByTestId('diff-view')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Close diff' }));
    expect(onClose).toHaveBeenCalled();
  });
});
