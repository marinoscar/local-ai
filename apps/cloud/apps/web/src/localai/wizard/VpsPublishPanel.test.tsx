// "Publish automatically": what blocks it, the enqueue, the journal of a finished run, the live states, Unpublish with its
// confirmation, and the axe checks. The transport is stubbed; nothing here touches a network.
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';

import { fakeClient, NODE_ID, vpsStatus } from './fixture';
import { VpsPublishPanel } from './VpsPublishPanel';
import type { VpsStatus } from './wizardTypes';

const apiError = (message: string, reason: string, status = 409) => Object.assign(new Error(message), { status, details: { reason } });

function renderPanel(status: VpsStatus | (() => VpsStatus) = vpsStatus(), overrides: Parameters<typeof fakeClient>[0] = {}) {
  const read = typeof status === 'function' ? status : () => status;
  const client = fakeClient({ vpsStatus: vi.fn().mockImplementation(async () => read()), ...overrides });
  const onSettled = vi.fn();
  const view = render(<VpsPublishPanel client={client} nodeId={NODE_ID} onSettled={onSettled} />);
  return { client, onSettled, ...view };
}

const finished = (action: 'publish' | 'unpublish', steps: VpsStatus['run']['steps'], extra: Partial<VpsStatus['run']> = {}) =>
  vpsStatus({ run: { state: 'succeeded', action, jobId: 'job-1', enqueuedAt: '2026-11-04T12:00:00.000Z', finishedAt: '2026-11-04T12:03:00.000Z', steps, ...extra } });

const PUBLISH_DONE: VpsStatus['run']['steps'] = [
  { name: 'dns_upsert', status: 'done', detail: 'Sent the A record (UPSERT, TTL 300) to Route53.' },
  { name: 'dns_wait', status: 'done', detail: 'Route53 reports INSYNC.' },
  { name: 'bootstrap_vhost', status: 'skipped', detail: 'A vhost file for this host is already in place.' },
  { name: 'certificate', status: 'done', detail: 'certbot issued the certificate.' },
  { name: 'final_vhost', status: 'done', detail: 'Wrote the Pattern B vhost.' },
  { name: 'validate', status: 'done', detail: 'GET /health answered 200 and GET /v1/models without a key answered 401.' },
];

describe('the Publish automatically button', () => {
  it('is enabled when nothing blocks it, and enqueues the publish', async () => {
    const { client } = renderPanel();
    const button = await screen.findByRole('button', { name: 'Publish automatically' });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    await waitFor(() => expect(client.vpsPublish).toHaveBeenCalledWith(NODE_ID));
    expect(client.vpsStatus.mock.calls.length).toBeGreaterThan(1); // refreshed straight away
  });

  it('is disabled with the blockers in plain words when something blocks it', async () => {
    renderPanel(
      vpsStatus({
        publish: {
          available: false,
          blockers: [
            { code: 'route53_not_configured', message: 'The Route53 key is not stored. Add it under Admin → Settings → VPS publishing.' },
            { code: 'agent_offline', message: 'The VPS stack-agent is not connected. Start the localai-vps-agent service on the VPS (runbook 10, part 10).' },
          ],
        },
      }),
    );
    const button = await screen.findByRole('button', { name: 'Publish automatically' });
    expect(button).toBeDisabled();
    const blockers = await screen.findByTestId('vps-blockers');
    expect(within(blockers).getByText(/Route53 key is not stored/)).toBeInTheDocument();
    expect(within(blockers).getByText(/stack-agent is not connected/)).toBeInTheDocument();
  });

  it('shows the server’s refusal when the click is refused', async () => {
    const { client } = renderPanel(vpsStatus(), { vpsPublish: vi.fn().mockRejectedValue(apiError('A publish or unpublish is already queued or running for this node.', 'job_active')) });
    const button = await screen.findByRole('button', { name: 'Publish automatically' });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    expect(await screen.findByText(/already queued or running/)).toBeInTheDocument();
    expect(client.vpsPublish).toHaveBeenCalledTimes(1);
  });

  it('is disabled while a run is in progress', async () => {
    renderPanel(vpsStatus({ run: { state: 'running', action: 'publish', jobId: 'job-1' } }));
    expect(await screen.findByRole('button', { name: 'Publish automatically' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Unpublish…' })).toBeDisabled();
  });
});

describe('the journal', () => {
  it('lists the planned steps as Waiting while a run is queued or running', async () => {
    renderPanel(vpsStatus({ run: { state: 'running', action: 'publish', jobId: 'job-1' } }));
    const journal = await screen.findByTestId('vps-journal');
    const rows = within(journal).getAllByRole('listitem');
    expect(rows).toHaveLength(6);
    for (const row of rows) expect(within(row).getByText('Waiting')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Last publish: Running');
  });

  it('shows every step of a finished run with a word and an icon, and the certificate end date', async () => {
    renderPanel(finished('publish', PUBLISH_DONE, { certificateExpiresAt: '2027-02-01T00:00:00.000Z' }));
    const journal = await screen.findByTestId('vps-journal');
    expect(within(within(journal).getByTestId('vps-step-dns_upsert')).getByText('Done')).toBeInTheDocument();
    expect(within(within(journal).getByTestId('vps-step-bootstrap_vhost')).getByText('Skipped')).toBeInTheDocument();
    expect(within(journal).getByText('Route53 reports INSYNC.')).toBeInTheDocument();
    expect(screen.getByText(/ends on 2027-02-01/)).toBeInTheDocument();
  });

  it('shows a failed step in words and the steps that did not run', async () => {
    renderPanel(
      finished('publish', [PUBLISH_DONE[0]!, PUBLISH_DONE[1]!, { name: 'bootstrap_vhost', status: 'failed', detail: 'nginx did not accept the bootstrap vhost. The previous file was restored and nginx is serving as before.' }], { state: 'failed' }),
    );
    const journal = await screen.findByTestId('vps-journal');
    expect(within(within(journal).getByTestId('vps-step-bootstrap_vhost')).getByText('Failed')).toBeInTheDocument();
    expect(within(journal).getByText(/previous file was restored/)).toBeInTheDocument();
    expect(within(within(journal).getByTestId('vps-step-certificate')).getByText('Not run')).toBeInTheDocument();
  });

  it('shows the job’s own error when it failed without a journal', async () => {
    renderPanel(vpsStatus({ run: { state: 'failed', action: 'publish', jobId: 'job-1', steps: [], error: 'The cloud did not issue the job credential.' } }));
    expect(await screen.findByText('The cloud did not issue the job credential.')).toBeInTheDocument();
  });

  it('shows the unpublish steps', async () => {
    renderPanel(
      finished('unpublish', [
        { name: 'vhost_remove', status: 'done', detail: 'Took the vhost out of service.' },
        { name: 'dns_delete', status: 'skipped', detail: 'The A record was already absent.' },
        { name: 'certificate_kept', status: 'skipped', detail: 'The certificate is left in place to expire; it is not revoked.' },
      ]),
    );
    const journal = await screen.findByRole('list', { name: 'Unpublish journal' });
    expect(within(journal).getAllByRole('listitem')).toHaveLength(3);
    expect(screen.getByText('Last unpublish: Finished')).toBeInTheDocument();
  });

  it('tells the checklist when a run it was watching settles', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let current = vpsStatus({ run: { state: 'running', action: 'publish', jobId: 'job-1' } });
      const { onSettled } = renderPanel(() => current);
      await screen.findByTestId('vps-journal');
      expect(onSettled).not.toHaveBeenCalled();
      current = finished('publish', PUBLISH_DONE);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3100);
      });
      await waitFor(() => expect(onSettled).toHaveBeenCalledTimes(1));
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('Unpublish', () => {
  it('asks first and does nothing on Cancel', async () => {
    const { client } = renderPanel();
    const open = await screen.findByRole('button', { name: 'Unpublish…' });
    await waitFor(() => expect(open).toBeEnabled());
    fireEvent.click(open);
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/certificate is left to expire/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(client.vpsUnpublish).not.toHaveBeenCalled();
  });

  it('enqueues the unpublish after the confirmation', async () => {
    const { client } = renderPanel();
    const open = await screen.findByRole('button', { name: 'Unpublish…' });
    await waitFor(() => expect(open).toBeEnabled());
    fireEvent.click(open);
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Unpublish' }));
    await waitFor(() => expect(client.vpsUnpublish).toHaveBeenCalledWith(NODE_ID));
  });
});

describe('accessibility and secrets', () => {
  async function noViolations(container: HTMLElement) {
    const result = await runAxe(container);
    expect(result.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);
  }

  it('has no axe violations when blocked, running and finished', async () => {
    const blocked = renderPanel(vpsStatus({ publish: { available: false, blockers: [{ code: 'agent_offline', message: 'The VPS stack-agent is not connected.' }] } }));
    await screen.findByTestId('vps-blockers');
    await noViolations(blocked.container);
    blocked.unmount();
    const running = renderPanel(vpsStatus({ run: { state: 'running', action: 'publish', jobId: 'job-1' } }));
    await screen.findByTestId('vps-journal');
    await noViolations(running.container);
    running.unmount();
    const done = renderPanel(finished('publish', PUBLISH_DONE));
    await screen.findByTestId('vps-journal');
    await noViolations(done.container);
  });

  it('puts nothing in storage or the URL', async () => {
    renderPanel(finished('publish', PUBLISH_DONE));
    await screen.findByTestId('vps-journal');
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
    expect(window.location.search).toBe('');
  });
});
