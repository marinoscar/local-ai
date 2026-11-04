// The Route53 key page: write-only (the fields are empty, the key is never read back or kept in the page after saving),
// the hint, replace and remove with a confirmation, errors, and the axe check. The transport is stubbed.
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import axe from 'axe-core';

import { Route53KeyPage } from './Route53KeyPage';
import type { Route53Client, Route53KeyStatus } from './route53Api';

const NONE: Route53KeyStatus = { configured: false, accessKeyHint: null, hostedZoneHint: null, updatedAt: null };
const STORED: Route53KeyStatus = { configured: true, accessKeyHint: '****WXYZ', hostedZoneHint: '****AB12', updatedAt: '2026-11-04T12:00:00.000Z' };

// Values built at run time: no key-shaped literal sits in the repository.
const random = (n: number) => Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'[b % 32]).join('');
const KEY = { accessKeyId: `AKIA${random(16)}`, secretAccessKey: random(40), hostedZoneId: `Z${random(14)}` };

function client(status: Route53KeyStatus = NONE, patch: Partial<{ [K in keyof Route53Client]: ReturnType<typeof vi.fn> }> = {}) {
  return { status: vi.fn().mockResolvedValue(status), save: vi.fn().mockResolvedValue(STORED), remove: vi.fn().mockResolvedValue(NONE), ...patch } as unknown as Route53Client & { [K in keyof Route53Client]: ReturnType<typeof vi.fn> };
}

async function fill() {
  fireEvent.change(screen.getByLabelText('Access key id'), { target: { value: KEY.accessKeyId } });
  fireEvent.change(screen.getByLabelText('Secret access key'), { target: { value: KEY.secretAccessKey } });
  fireEvent.change(screen.getByLabelText('Hosted zone id'), { target: { value: KEY.hostedZoneId } });
}

describe('Route53KeyPage', () => {
  it('says no key is stored and keeps Save disabled until all three fields are filled', async () => {
    render(<Route53KeyPage client={client()} />);
    expect(await screen.findByText('No key stored')).toBeInTheDocument();
    const save = screen.getByRole('button', { name: 'Save key' });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Access key id'), { target: { value: KEY.accessKeyId } });
    expect(save).toBeDisabled();
    await fill();
    expect(save).toBeEnabled();
  });

  it('masks the secret fields', async () => {
    render(<Route53KeyPage client={client()} />);
    await screen.findByText('No key stored');
    expect(screen.getByLabelText('Secret access key')).toHaveAttribute('type', 'password');
  });

  it('saves the three values, then empties the form and says it cannot be shown again', async () => {
    const c = client();
    render(<Route53KeyPage client={c} />);
    await screen.findByText('No key stored');
    await fill();
    fireEvent.click(screen.getByRole('button', { name: 'Save key' }));
    await waitFor(() => expect(c.save).toHaveBeenCalledWith(KEY));
    expect(await screen.findByText('Key stored')).toBeInTheDocument();
    expect(screen.getByText(/cannot be shown again/)).toBeInTheDocument();
    for (const label of ['Access key id', 'Secret access key', 'Hosted zone id']) expect(screen.getByLabelText(label)).toHaveValue('');
    // The key is nowhere on the page.
    expect(document.body.textContent).not.toContain(KEY.secretAccessKey);
    expect(document.body.textContent).not.toContain(KEY.accessKeyId);
  });

  it('shows the stored key only as a hint and offers Replace and Remove', async () => {
    render(<Route53KeyPage client={client(STORED)} />);
    expect(await screen.findByText('Key stored')).toBeInTheDocument();
    expect(screen.getByText(/Access key \*\*\*\*WXYZ, saved 2026-11-04/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Replace key' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Remove key' })).toBeInTheDocument();
  });

  it('removes the key only after a confirmation', async () => {
    const c = client(STORED);
    render(<Route53KeyPage client={c} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Remove key' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(c.remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Remove key' }));
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Remove key' }));
    await waitFor(() => expect(c.remove).toHaveBeenCalledTimes(1));
    expect(await screen.findByText('No key stored')).toBeInTheDocument();
  });

  it('shows the server’s message when saving fails, and keeps what was typed so it can be fixed', async () => {
    const c = client(NONE, { save: vi.fn().mockRejectedValue(new Error('accessKeyId must be an AWS access key id')) });
    render(<Route53KeyPage client={c} />);
    await screen.findByText('No key stored');
    await fill();
    fireEvent.click(screen.getByRole('button', { name: 'Save key' }));
    expect(await screen.findByText('accessKeyId must be an AWS access key id')).toBeInTheDocument();
    expect(screen.getByLabelText('Hosted zone id')).toHaveValue(KEY.hostedZoneId);
  });

  it('offers to retry when the status cannot be loaded', async () => {
    const c = client(NONE, { status: vi.fn().mockRejectedValueOnce(new Error('x')).mockResolvedValue(NONE) });
    render(<Route53KeyPage client={c} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('No key stored')).toBeInTheDocument();
  });

  it('shows the IAM policy with placeholders only', async () => {
    render(<Route53KeyPage client={client()} />);
    const policy = await screen.findByLabelText('IAM policy example');
    expect(policy.textContent).toContain('route53:ChangeResourceRecordSets');
    expect(policy.textContent).toContain('<ROUTE53_ZONE_ID>');
  });

  it('has no axe violations (empty, stored)', async () => {
    const empty = render(<Route53KeyPage client={client()} />);
    await screen.findByText('No key stored');
    expect((await axe.run(empty.container, { rules: { 'color-contrast': { enabled: false } } })).violations.map((v) => v.id)).toEqual([]);
    empty.unmount();
    const stored = render(<Route53KeyPage client={client(STORED)} />);
    await screen.findByText('Key stored');
    expect((await axe.run(stored.container, { rules: { 'color-contrast': { enabled: false } } })).violations.map((v) => v.id)).toEqual([]);
  });
});
