import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';

import { ApiError } from '@marinoscar/platform-web/core';

import { ApproveDialog } from './ApproveDialog';
import { BAD_MODELS_YAML, fakeCatalog, files, summary, wireframeVersions } from './fixture';

function validDraft() {
  const fake = fakeCatalog([...wireframeVersions().slice(1), summary('2026.11.03-1', 'valid', { source: 'editor' })]);
  // A different models.yaml than the approved one, so the dialog has something to summarise.
  fake.rows.get('2026.11.03-1')!.files = files('2026.11.03-1', BAD_MODELS_YAML.replace('main', 'b'.repeat(40)));
  return fake;
}

describe('ApproveDialog', () => {
  it('asks "Approve <version> for all nodes?" and shows the diff summary before the confirm button', async () => {
    const fake = validDraft();
    render(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={() => undefined} onApproved={() => undefined} />);
    expect(screen.getByRole('dialog', { name: 'Approve 2026.11.03-1 for all nodes?' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    const summaryBox = await screen.findByText('Changes since 2026.11.02-1');
    expect(summaryBox).toBeInTheDocument();
    expect(screen.getByTestId('approve-diff-summary')).toHaveTextContent('2 files changed, +2 -2 lines');
    const list = screen.getByRole('list', { name: 'Changed files' });
    expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual(['CATALOG_VERSION changed · +1 -1', 'models.yaml changed · +1 -1']);
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();
    expect(fake.calls.get).toHaveBeenCalledWith('2026.11.03-1', 'approved');
  });

  it('has the "Push to nodes now" checkbox on and sends notifyNodes accordingly', async () => {
    const fake = validDraft();
    const onApproved = vi.fn();
    render(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={() => undefined} onApproved={onApproved} />);
    const box = screen.getByRole('checkbox', { name: 'Push to nodes now' });
    expect(box).toBeChecked();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(onApproved).toHaveBeenCalled());
    expect(fake.calls.approve).toHaveBeenCalledWith('2026.11.03-1', true);
    expect(onApproved.mock.calls[0]![0].supersedes).toBe('2026.11.02-1');
  });

  it('sends notifyNodes false when the box is cleared', async () => {
    const fake = validDraft();
    render(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={() => undefined} onApproved={() => undefined} />);
    fireEvent.click(screen.getByRole('checkbox', { name: 'Push to nodes now' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(fake.calls.approve).toHaveBeenCalledWith('2026.11.03-1', false));
  });

  it('says so when nothing is approved yet', async () => {
    const fake = fakeCatalog([summary('2026.11.03-1', 'valid')]);
    render(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={() => undefined} onApproved={() => undefined} />);
    expect(await screen.findByText('No catalog is approved yet; this will be the first.')).toBeInTheDocument();
  });

  it('shows the server refusal and stays open', async () => {
    const fake = validDraft();
    fake.calls.approve.mockRejectedValueOnce(new ApiError('Catalog 2026.11.03-1 is draft; only a valid catalog can be approved.', 409, undefined, { reason: 'not_valid' }));
    const onApproved = vi.fn();
    render(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={() => undefined} onApproved={onApproved} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));
    expect(await screen.findByText('Catalog 2026.11.03-1 is draft; only a valid catalog can be approved.')).toBeInTheDocument();
    expect(onApproved).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();
  });

  it('still lets the admin approve when the comparison fails, with a warning', async () => {
    const fake = validDraft();
    fake.calls.get.mockRejectedValue(new Error('network'));
    render(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={() => undefined} onApproved={() => undefined} />);
    expect(await screen.findByText('Could not compare with the approved catalog. You can still approve.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();
  });

  it('renders nothing without a version, and Cancel closes', () => {
    const fake = validDraft();
    const onClose = vi.fn();
    const { rerender } = render(<ApproveDialog client={fake.api} version={null} onClose={onClose} onApproved={() => undefined} />);
    expect(screen.queryByRole('dialog')).toBeNull();
    rerender(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={onClose} onApproved={() => undefined} />);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalled();
  });

  it('has no axe violations', async () => {
    const fake = validDraft();
    render(<ApproveDialog client={fake.api} version="2026.11.03-1" onClose={() => undefined} onApproved={() => undefined} />);
    await screen.findByText('Changes since 2026.11.02-1');
    expect((await runAxe(screen.getByRole('dialog'))).violations).toEqual([]);
  });
});
