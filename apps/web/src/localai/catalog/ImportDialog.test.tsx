import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';

import { ApiError } from '@marinoscar/platform-web/core';

import { fakeCatalog } from './fixture';
import { ImportDialog } from './ImportDialog';

const type = (value: string) => fireEvent.change(screen.getByLabelText('Release tag'), { target: { value } });

describe('ImportDialog', () => {
  it('queues the import for a valid tag', async () => {
    const fake = fakeCatalog();
    const onQueued = vi.fn();
    render(<ImportDialog client={fake.api} open onClose={() => undefined} onQueued={onQueued} />);
    type(' catalog-2026.11.05-1 ');
    fireEvent.click(screen.getByRole('button', { name: 'Import' }));
    await waitFor(() => expect(onQueued).toHaveBeenCalledWith('catalog-2026.11.05-1', '2026.11.05-1'));
    expect(fake.calls.importRelease).toHaveBeenCalledWith('catalog-2026.11.05-1');
  });

  it('refuses a malformed tag before any request, with an example', () => {
    const fake = fakeCatalog();
    render(<ImportDialog client={fake.api} open onClose={() => undefined} onQueued={() => undefined} />);
    for (const tag of ['', 'catalog-stable', '2026.11.05-1', 'catalog-2026.02.30-1']) {
      type(tag);
      fireEvent.click(screen.getByRole('button', { name: 'Import' }));
      expect(screen.getByText('Use catalog-YYYY.MM.DD-N, for example catalog-2026.11.02-1.')).toBeInTheDocument();
    }
    expect(fake.calls.importRelease).not.toHaveBeenCalled();
  });

  it('shows a 409 as "This version already exists."', async () => {
    const fake = fakeCatalog();
    const onQueued = vi.fn();
    render(<ImportDialog client={fake.api} open onClose={() => undefined} onQueued={onQueued} />);
    type('catalog-2026.11.02-1');
    fireEvent.click(screen.getByRole('button', { name: 'Import' }));
    expect(await screen.findByText('This version already exists.')).toBeInTheDocument();
    expect(onQueued).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Import' })).toBeEnabled();
  });

  it('shows any other failure with the server sentence', async () => {
    const fake = fakeCatalog();
    fake.calls.importRelease.mockRejectedValueOnce(new ApiError('The tag must look like catalog-2026.11.02-1.', 400));
    render(<ImportDialog client={fake.api} open onClose={() => undefined} onQueued={() => undefined} />);
    type('catalog-2026.11.09-1');
    fireEvent.click(screen.getByRole('button', { name: 'Import' }));
    expect(await screen.findByText('The tag must look like catalog-2026.11.02-1.')).toBeInTheDocument();
  });

  it('renders nothing when closed, and has no axe violations when open', async () => {
    const fake = fakeCatalog();
    const { rerender } = render(<ImportDialog client={fake.api} open={false} onClose={() => undefined} onQueued={() => undefined} />);
    expect(screen.queryByRole('dialog')).toBeNull();
    rerender(<ImportDialog client={fake.api} open onClose={() => undefined} onQueued={() => undefined} />);
    expect((await runAxe(screen.getByRole('dialog'))).violations).toEqual([]);
  });
});
