import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import axe from 'axe-core';

import { ConfirmDialog, TypedConfirmDialog } from './ConfirmDialog';

describe('TypedConfirmDialog', () => {
  const setup = (onConfirm = vi.fn().mockResolvedValue(null), expected = 'ai-n2') => {
    const onClose = vi.fn();
    render(
      <TypedConfirmDialog open title="Retire ai-n2?" confirmLabel="Retire node" expected={expected} onConfirm={onConfirm} onClose={onClose}>
        <p>This cannot be undone.</p>
      </TypedConfirmDialog>,
    );
    return { onConfirm, onClose, confirm: screen.getByRole('button', { name: 'Retire node' }), input: screen.getByLabelText(`Type ${expected} to confirm`) };
  };

  it('keeps the confirm button disabled until the exact text is typed (case, spaces and prefixes all count)', () => {
    const { confirm, input } = setup();
    expect(confirm).toBeDisabled();
    for (const wrong of ['a', 'ai-n', 'ai-n22', 'AI-N2', ' ai-n2', 'ai-n2 ', 'ai-n3', '']) {
      fireEvent.change(input, { target: { value: wrong } });
      expect(confirm).toBeDisabled();
    }
    fireEvent.change(input, { target: { value: 'ai-n2' } });
    expect(confirm).toBeEnabled();
  });

  it('runs onConfirm only when enabled and stays open with the refusal it returns', async () => {
    const { confirm, input, onConfirm } = setup(vi.fn().mockResolvedValue('The cloud said no.'));
    fireEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
    fireEvent.change(input, { target: { value: 'ai-n2' } });
    fireEvent.click(confirm);
    expect(await screen.findByRole('alert')).toHaveTextContent('The cloud said no.');
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('Cancel closes without confirming', () => {
    const { onClose, onConfirm } = setup();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('is accessible', async () => {
    setup();
    const result = await axe.run(screen.getByRole('dialog'), { rules: { 'color-contrast': { enabled: false } } });
    expect(result.violations.map((violation) => violation.id)).toEqual([]);
  });
});

describe('ConfirmDialog', () => {
  it('disables the buttons while the action runs and reports success by returning null', async () => {
    let finish: (value: null) => void = () => undefined;
    const onConfirm = vi.fn(() => new Promise<null>((resolve) => { finish = resolve; }));
    render(<ConfirmDialog open title="Go?" confirmLabel="Go" onConfirm={onConfirm} onClose={() => undefined} />);
    fireEvent.click(screen.getByRole('button', { name: 'Go' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Go' })).toBeDisabled());
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();
    finish(null);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Go' })).toBeEnabled());
  });
});
