// The node detail page end to end over mocked transports (the real API routes are proven by the API's specs): the header,
// the six tabs, every action with its permission and confirmation, the command line (queued -> running -> done, cancel
// only while queued), the refusal texts of an Offline node, the one-time credential, the tailnet reveal, retire, and axe.
import { ApiError } from '@marinoscar/platform-web/core';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import axe from 'axe-core';
import { MemoryRouter, Route, Routes } from 'react-router-dom';

import { cancelCommand, getCommand, listCommands, sendCommand } from '../api/commands';
import { getNodeDetail, patchNode, replaceNodeCredential, retireNode, revealTailnetIp } from '../api/nodes';
import type { NodeDetail } from '../api/types';
import { FAKE_TAILNET_IP, LAB_NOW, MODEL_ABSENT, MODEL_LOADED, MODEL_READY, MODEL_TOO_BIG, commandFixture, detailFixture } from './fixture';
import { NodeDetailPage } from './NodeDetailPage';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));
vi.mock('../api/nodes', () => ({ getNodeDetail: vi.fn(), revealTailnetIp: vi.fn(), patchNode: vi.fn(), replaceNodeCredential: vi.fn(), retireNode: vi.fn() }));
vi.mock('../api/commands', () => ({ sendCommand: vi.fn(), getCommand: vi.fn(), cancelCommand: vi.fn(), listCommands: vi.fn() }));

const VIEWER = ['lab:read'];
const CONTRIBUTOR = ['lab:read', 'lab:operate'];
const ADMIN = ['lab:read', 'lab:operate', 'lab:admin'];

function renderPage(detail: NodeDetail = detailFixture(), held: string[] = ADMIN, initial = '/lab/nodes/ai-n2') {
  permissions = held;
  vi.mocked(getNodeDetail).mockResolvedValue(detail);
  return render(
    <MemoryRouter initialEntries={[initial]}>
      <Routes>
        <Route path="/lab/nodes/:nodeId" element={<NodeDetailPage now={LAB_NOW} commandPollMs={15} />} />
        <Route path="/lab" element={<p>The lab page</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

async function openTab(name: string) {
  fireEvent.click(await screen.findByRole('tab', { name }));
}

const ACTION_NAMES = /^(load|unload|set default|download|sync now|preview|verify|restart|fetch|rotate key|replace credential|retire node|go offline|update agent|refresh status|save|reveal|change|set…|clear)/i;
const actionButtons = () => screen.queryAllByRole('button').filter((button) => ACTION_NAMES.test(button.textContent ?? '') || /more actions/i.test(button.getAttribute('aria-label') ?? ''));

beforeEach(() => {
  vi.mocked(getNodeDetail).mockReset();
  vi.mocked(listCommands).mockResolvedValue({ items: [], page: 1, pageSize: 20, total: 0, totalPages: 0 });
  vi.mocked(sendCommand).mockReset();
  vi.mocked(getCommand).mockReset();
  vi.mocked(cancelCommand).mockReset();
  vi.mocked(patchNode).mockReset();
  vi.mocked(replaceNodeCredential).mockReset();
  vi.mocked(retireNode).mockReset();
  vi.mocked(revealTailnetIp).mockReset();
});

describe('the header', () => {
  it('shows the id and name, the three chips, the profile / agent / llama line and how fresh the data is', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: 'ai-n2 · MSI Stealth' })).toBeInTheDocument();
    expect(screen.getByTestId('health-chip')).toHaveTextContent('Healthy');
    expect(screen.getByTestId('mode-chip')).toHaveTextContent('Online');
    expect(screen.getByText('connectivity: full')).toBeInTheDocument();
    expect(screen.getByTestId('node-meta')).toHaveTextContent('profile cuda-12gb · agent 0.4.1 · llama f3586e66b-cuda · router up · models_max 1');
    expect(screen.getByTestId('last-update')).toHaveTextContent('last update 21 s ago');
    expect(screen.getByRole('link', { name: 'Back to the lab' })).toHaveAttribute('href', '/lab');
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Models', 'Ark', 'Services', 'Logs', 'Keys', 'Settings']);
  });

  it('Refresh status queues status.refresh and the line walks queued -> running -> Done, then the node is read again', async () => {
    const queued = commandFixture({ type: 'localai.status.refresh', params: {} });
    vi.mocked(sendCommand).mockResolvedValue(queued);
    vi.mocked(getCommand)
      .mockResolvedValueOnce({ ...queued, status: 'running', claimedAt: '2026-11-03T12:00:05.000Z' })
      .mockResolvedValue({ ...queued, status: 'succeeded', claimedAt: '2026-11-03T12:00:05.000Z', finishedAt: '2026-11-03T12:00:09.000Z', result: { seq: 4, reported_at: '2026-11-03T12:00:09.000Z' } });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Refresh status' }));
    expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'status.refresh', {});
    expect(await screen.findByTestId('command-status')).toHaveTextContent('Waiting for ai-n2 (checks every few seconds)');
    await waitFor(() => expect(screen.getByTestId('command-status')).toHaveTextContent('Running'));
    await waitFor(() => expect(screen.getByTestId('command-status')).toHaveTextContent('Done'));
    // The first read at mount plus one refresh when the command settled.
    await waitFor(() => expect(getNodeDetail).toHaveBeenCalledTimes(2));
  });

  it('adopts a command that was already queued when the page opened', async () => {
    vi.mocked(listCommands).mockResolvedValue({ items: [commandFixture({ status: 'queued' }), commandFixture({ status: 'succeeded', finishedAt: '2026-11-03T12:00:01.000Z' })], page: 1, pageSize: 20, total: 2, totalPages: 1 });
    vi.mocked(getCommand).mockImplementation(async () => commandFixture({ status: 'queued' }));
    renderPage();
    const lines = await screen.findAllByTestId('command-line');
    expect(lines).toHaveLength(1);
    expect(lines[0]).toHaveAttribute('data-status', 'queued');
    expect(screen.getByText('Load qwen3.5-9b-q4km', { exact: false })).toBeInTheDocument();
  });
});

describe('Models tab', () => {
  it('lists the four models with state, disk, fit, and the right actions per row (admin)', async () => {
    renderPage();
    const loaded = await screen.findByTestId(`model-row-${MODEL_LOADED}`);
    expect(within(loaded).getByText('loaded')).toBeInTheDocument();
    expect(within(loaded).getByRole('button', { name: 'Unload' })).toBeInTheDocument();
    expect(within(loaded).getByText('Default')).toBeInTheDocument();
    expect(within(loaded).queryByRole('button', { name: 'Set default' })).toBeNull();

    const ready = screen.getByTestId(`model-row-${MODEL_READY}`);
    expect(within(ready).getByRole('button', { name: 'Load' })).toBeInTheDocument();
    expect(within(ready).getByRole('button', { name: 'Set default' })).toBeInTheDocument();

    const absent = screen.getByTestId(`model-row-${MODEL_ABSENT}`);
    expect(within(absent).getByRole('button', { name: 'Download 5.1 GB' })).toBeInTheDocument();
    expect(within(absent).queryByRole('button', { name: 'Load' })).toBeNull();

    const tooBig = screen.getByTestId(`model-row-${MODEL_TOO_BIG}`);
    expect(within(tooBig).getByText('(needs 49 GB)')).toBeInTheDocument();
    expect(within(tooBig).queryByRole('button', { name: /download|load/i })).toBeNull();
  });

  it('Load sends model.load for that model and disables the button while it is in flight', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture());
    vi.mocked(getCommand).mockImplementation(async () => commandFixture({ status: 'queued' }));
    renderPage();
    const row = await screen.findByTestId(`model-row-${MODEL_READY}`);
    fireEvent.click(within(row).getByRole('button', { name: 'Load' }));
    expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'model.load', { model_id: MODEL_READY });
    await waitFor(() => expect(within(row).getByRole('button', { name: 'Load' })).toBeDisabled());
  });

  it('Unload and Set default send their commands', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ status: 'queued' }));
    renderPage();
    fireEvent.click(within(await screen.findByTestId(`model-row-${MODEL_LOADED}`)).getByRole('button', { name: 'Unload' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'model.unload', { model_id: MODEL_LOADED }));
    fireEvent.click(within(screen.getByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Set default' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'model.set_default', { model_id: MODEL_READY }));
  });

  it('Download asks first and shows the planned size', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.model.download', status: 'queued', params: { model_id: MODEL_ABSENT } }));
    renderPage();
    fireEvent.click(within(await screen.findByTestId(`model-row-${MODEL_ABSENT}`)).getByRole('button', { name: 'Download 5.1 GB' }));
    const dialog = await screen.findByRole('dialog', { name: `Download ${MODEL_ABSENT}?` });
    expect(dialog).toHaveTextContent('Planned size: 5.1 GB');
    expect(sendCommand).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Download' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'model.download', { model_id: MODEL_ABSENT }));
  });

  it('Delete files needs lab:admin and the exact model id typed; a different text keeps the button disabled', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.model.delete', status: 'queued', params: {} }));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: `More actions for ${MODEL_READY}` }));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete files…' }));
    const dialog = await screen.findByRole('dialog', { name: `Delete the files of ${MODEL_READY}?` });
    expect(dialog).toHaveTextContent('internal, ark-1a2b3c4d');
    const confirm = within(dialog).getByRole('button', { name: 'Delete files' });
    expect(confirm).toBeDisabled();
    const input = within(dialog).getByLabelText(`Type ${MODEL_READY} to confirm`);
    for (const wrong of [MODEL_LOADED, MODEL_READY.toUpperCase(), `${MODEL_READY} `, MODEL_READY.slice(0, -1)]) {
      fireEvent.change(input, { target: { value: wrong } });
      expect(confirm).toBeDisabled();
    }
    fireEvent.change(input, { target: { value: MODEL_READY } });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);
    await waitFor(() =>
      expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'model.delete', { model_id: MODEL_READY, roots: ['internal', 'ark-1a2b3c4d'], confirm: MODEL_READY }),
    );
  });

  it('says so when the node has not reported a snapshot', async () => {
    renderPage(detailFixture({ lastState: null }));
    expect(await screen.findByText('ai-n2 has not reported its models yet.')).toBeInTheDocument();
  });
});

describe('cancel', () => {
  it('offers Cancel only while the command is queued, and cancels it', async () => {
    const queued = commandFixture({ status: 'queued' });
    vi.mocked(sendCommand).mockResolvedValue(queued);
    vi.mocked(getCommand).mockImplementation(async () => queued);
    vi.mocked(cancelCommand).mockResolvedValue({ ...queued, status: 'cancelled', finishedAt: '2026-11-03T12:00:10.000Z' });
    renderPage();
    fireEvent.click(within(await screen.findByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' }));
    const cancel = await screen.findByRole('button', { name: /^Cancel: Load/ });
    fireEvent.click(cancel);
    expect(cancelCommand).toHaveBeenCalledWith(queued.id);
    await waitFor(() => expect(screen.getByTestId('command-status')).toHaveTextContent('Cancelled'));
    expect(screen.queryByRole('button', { name: /^Cancel: Load/ })).toBeNull();
  });

  it('has no Cancel once the node is running it, and a 409 on cancel says the node already took it', async () => {
    const queued = commandFixture({ status: 'queued' });
    vi.mocked(sendCommand).mockResolvedValue(queued);
    vi.mocked(getCommand).mockImplementation(async () => ({ ...queued, status: 'running', claimedAt: '2026-11-03T12:00:03.000Z' }));
    vi.mocked(cancelCommand).mockRejectedValue(new ApiError('Conflict', 409));
    renderPage();
    fireEvent.click(within(await screen.findByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' }));
    fireEvent.click(await screen.findByRole('button', { name: /^Cancel: Load/ }));
    expect(await screen.findByTestId('command-notice')).toHaveTextContent('The node already took this command');
    await waitFor(() => expect(screen.getByTestId('command-status')).toHaveTextContent('Running'));
    expect(screen.queryByRole('button', { name: /^Cancel: Load/ })).toBeNull();
  });

  it('a failed command shows the plain-language fix for its error code', async () => {
    const queued = commandFixture({ status: 'queued' });
    vi.mocked(sendCommand).mockResolvedValue(queued);
    vi.mocked(getCommand).mockImplementation(async () => ({ ...queued, status: 'failed', finishedAt: '2026-11-03T12:00:09.000Z', error: { code: 'LAI-LLM-011', message: 'raw agent text' } }));
    renderPage();
    fireEvent.click(within(await screen.findByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' }));
    const problem = await screen.findByTestId('command-problem');
    expect(problem).toHaveTextContent('Unload a model first, or allow unloading others (LAI-LLM-011)');
    expect(problem).not.toHaveTextContent('raw agent text');
  });
});

describe('refusals', () => {
  it('an Offline node explains LAI-CLD-034 and every command button is disabled', async () => {
    renderPage(detailFixture({ mode: 'offline', health: 'quiet' }));
    const banner = await screen.findByTestId('refusal-banner');
    expect(banner).toHaveTextContent('This computer is in Offline mode. Only someone at it can switch it back.');
    expect(banner).toHaveTextContent('LAI-CLD-034');
    for (const name of ['Refresh status', 'Go offline…']) expect(screen.getByRole('button', { name })).toBeDisabled();
    expect(within(screen.getByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' })).toBeDisabled();
    expect(within(screen.getByTestId(`model-row-${MODEL_ABSENT}`)).getByRole('button', { name: /Download/ })).toBeDisabled();
  });

  it('a Survival node says Survival; a pending node says it is not enrolled yet (LAI-CLD-035)', async () => {
    const { unmount } = renderPage(detailFixture({ mode: 'survival' }));
    expect(await screen.findByTestId('refusal-banner')).toHaveTextContent('Survival mode');
    unmount();
    renderPage(detailFixture({ phase: 'pending', mode: null }));
    expect(await screen.findByTestId('refusal-banner')).toHaveTextContent('LAI-CLD-035');
  });

  it('a type missing from the node allow list is disabled with the LAI-CLD-057 text', async () => {
    renderPage(detailFixture({ acceptedTypes: ALL_BUT_LOAD }));
    const load = within(await screen.findByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' });
    expect(load).toBeDisabled();
    expect(load.closest('span')).toHaveAttribute('title', 'ai-n2 does not accept this command (disabled on the node)');
  });

  it('a 422 LAI-CLD-034 from the server (the snapshot changed under the page) shows the same explanation', async () => {
    vi.mocked(sendCommand).mockRejectedValue(new ApiError('Node in offline mode', 422, undefined, { code: 'LAI-CLD-034', mode: 'offline' }));
    renderPage();
    fireEvent.click(within(await screen.findByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' }));
    expect(await screen.findByTestId('command-notice')).toHaveTextContent('This computer is in Offline mode. Only someone at it can switch it back. (LAI-CLD-034)');
  });

  it('shows a refusal for a model that is not on the node', async () => {
    vi.mocked(sendCommand).mockRejectedValue(new ApiError('nope', 422, undefined, { reason: 'model_not_on_node' }));
    renderPage();
    fireEvent.click(within(await screen.findByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' }));
    expect(await screen.findByTestId('command-notice')).toHaveTextContent('This model is not on ai-n2. Download it first.');
  });
});

const ALL_BUT_LOAD = detailFixture().acceptedTypes!.filter((type) => type !== 'model.load');

describe('Update agent', () => {
  const newer = (patch: Partial<NodeDetail> = {}) => detailFixture({ agentLatestVersion: '0.4.2', ...patch });

  it('is not rendered without lab:admin, even when an update exists', async () => {
    renderPage(newer(), CONTRIBUTOR);
    await screen.findByRole('button', { name: 'Refresh status' });
    expect(screen.queryByRole('button', { name: /Update agent/ })).toBeNull();
  });

  it('is not rendered when agent.latestVersion is not set', async () => {
    renderPage(detailFixture({ agentLatestVersion: null }));
    await screen.findByRole('button', { name: 'Refresh status' });
    expect(screen.queryByRole('button', { name: /Update agent/ })).toBeNull();
  });

  it('is disabled when the latest version is not newer, with the reason', async () => {
    renderPage(detailFixture({ agentLatestVersion: '0.4.1' }));
    const button = await screen.findByRole('button', { name: /Update agent/ });
    expect(button).toBeDisabled();
    expect(button.closest('span')).toHaveAttribute('title', 'ai-n2 already runs 0.4.1');
  });

  it('is disabled when the node is not online', async () => {
    renderPage(newer({ mode: 'offline', health: 'quiet' }));
    expect(await screen.findByRole('button', { name: /Update agent/ })).toBeDisabled();
  });

  it('is enabled for a newer version: the confirmation shows from -> to and enqueues exactly one agent.update', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.agent.update', params: { version: '0.4.2' } }));
    renderPage(newer());
    fireEvent.click(await screen.findByRole('button', { name: 'Update agent 0.4.1 → 0.4.2…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Update the agent?' });
    expect(dialog).toHaveTextContent('Update ai-n2 from 0.4.1 to 0.4.2? The agent restarts; models keep running.');
    expect(sendCommand).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Update agent' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledTimes(1));
    expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'agent.update', { version: '0.4.2' });
  });
});

describe('Go offline', () => {
  it('warns that only someone at the node can bring it back, then sends mode.offline', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.mode.offline', params: {} }));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Go offline…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Take ai-n2 offline?' });
    expect(dialog).toHaveTextContent('Only someone at the node can bring it back online.');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Go offline' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'mode.offline', {}));
  });

  it('keeps the dialog open with the server refusal', async () => {
    vi.mocked(sendCommand).mockRejectedValue(new ApiError('Forbidden', 403));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Go offline…' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Go offline' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('You do not have permission to do this.');
  });
});

describe('Ark tab', () => {
  it('shows one panel per root with tiers, counters, space, and the last sync error with its plain fix', async () => {
    renderPage();
    await openTab('Ark');
    const internal = await screen.findByTestId('ark-root-internal');
    expect(within(internal).getByText('Essential (t1) ok')).toBeInTheDocument();
    expect(within(internal).getByText('Useful (t2) partial')).toBeInTheDocument();
    expect(within(internal).getByTestId('ark-items')).toHaveTextContent('3 missing · 1 failed · 2 skipped');
    expect(within(internal).getByText(/372\.5 GB used/)).toBeInTheDocument();
    expect(within(internal).getByTestId('ark-chip')).toHaveAttribute('data-colour', 'red');
    const drive = screen.getByTestId('ark-root-ark-1a2b3c4d');
    expect(within(drive).getByTestId('ark-drive-age')).toHaveTextContent('41 days ago');
    const problem = screen.getByTestId('ark-sync-problem');
    expect(problem).toHaveTextContent('Download failed after all retries');
    expect(problem).toHaveTextContent('Check the internet connection');
    expect(problem).toHaveTextContent('LAI-SYN-031');
  });

  it('Sync now, Preview (dry run) and Verify queue ark.sync and ark.verify', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.ark.sync', status: 'queued', params: {} }));
    renderPage(detailFixture(), CONTRIBUTOR);
    await openTab('Ark');
    fireEvent.click(await screen.findByRole('button', { name: 'Preview (dry run)' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'ark.sync', { dry_run: true }));
    fireEvent.click(screen.getByRole('button', { name: 'Verify' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'ark.verify', {}));
  });

  it('shows the dry-run plan when the preview settles', async () => {
    const queued = commandFixture({ type: 'localai.ark.sync', status: 'queued', params: { dry_run: true } });
    vi.mocked(sendCommand).mockResolvedValue(queued);
    vi.mocked(getCommand).mockResolvedValue({
      ...queued,
      status: 'succeeded',
      finishedAt: '2026-11-03T12:00:09.000Z',
      result: { sync_run_id: 'run-9', started: true, plan: { items_to_download: 3, bytes_to_download: 5_368_709_120, items_skipped_budget: 0 } },
    });
    renderPage();
    await openTab('Ark');
    fireEvent.click(await screen.findByRole('button', { name: 'Preview (dry run)' }));
    await waitFor(() => expect(screen.getByTestId('command-line')).toHaveTextContent('3 items to download (5 GB)'));
  });

  it('shows an unplugged drive row without details', async () => {
    const detail = detailFixture();
    detail.ark = [detail.ark[0]!, { rootId: 'ark-drive', kind: 'ark_drive', present: false, colour: 'amber', label: 'Drive not plugged in · last synced 6 days ago', driveAgeDays: 6 }];
    renderPage(detail);
    await openTab('Ark');
    expect(await screen.findByText('The drive is not plugged in, so there are no details.')).toBeInTheDocument();
  });
});

describe('Services tab', () => {
  it('shows the five services and a Restart button for llama, kiwix and whisper only', async () => {
    renderPage();
    await openTab('Services');
    expect(await screen.findByTestId('service-row-llama')).toHaveTextContent('Running');
    expect(screen.getByTestId('service-row-whisper')).toHaveTextContent('Failed');
    for (const name of ['llama', 'kiwix', 'whisper']) expect(within(screen.getByTestId(`service-row-${name}`)).getByRole('button', { name: `Restart ${name}` })).toBeInTheDocument();
    for (const name of ['sync', 'drill']) expect(within(screen.getByTestId(`service-row-${name}`)).queryByRole('button')).toBeNull();
  });

  it('Restart queues service.restart for that service', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.service.restart', params: { service: 'kiwix' } }));
    renderPage();
    await openTab('Services');
    fireEvent.click(await screen.findByRole('button', { name: 'Restart kiwix' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'service.restart', { service: 'kiwix' }));
  });
});

describe('Logs tab', () => {
  it('fetches logs.tail with the chosen unit and lines and shows the lines as plain text', async () => {
    const queued = commandFixture({ type: 'localai.logs.tail', params: { unit: 'llama', lines: 50 } });
    vi.mocked(sendCommand).mockResolvedValue(queued);
    vi.mocked(getCommand).mockResolvedValue({
      ...queued,
      status: 'succeeded',
      finishedAt: '2026-11-03T12:00:09.000Z',
      result: { unit: 'llama', lines: ['Nov 03 started', 'Authorization: Bearer [redacted]', '<img src=x onerror="alert(1)">'], truncated: true },
    });
    const { container } = renderPage();
    await openTab('Logs');
    expect(await screen.findByText('Logs are cleaned of keys and tokens before they leave the computer.')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Unit'), { target: { value: 'llama' } });
    fireEvent.change(screen.getByLabelText('Lines'), { target: { value: '50' } });
    fireEvent.click(screen.getByRole('button', { name: 'Fetch' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'logs.tail', { unit: 'llama', lines: 50 }));
    const viewer = await screen.findByTestId('logs-viewer');
    expect(viewer.textContent).toContain('Nov 03 started');
    expect(viewer.textContent).toContain('<img src=x onerror="alert(1)">');
    expect(container.querySelector('img')).toBeNull();
    expect(screen.getByTestId('logs-meta')).toHaveTextContent('3 lines (older lines were cut) · 1 value hidden as [redacted]');
  });

  it('offers units agent, llama, kiwix, whisper, sync and drill and 50 / 200 / 500 lines', async () => {
    renderPage();
    await openTab('Logs');
    const unit = (await screen.findByLabelText('Unit')) as HTMLSelectElement;
    expect(unit).toHaveValue('agent');
    expect(Array.from(unit.options).map((option) => option.value)).toEqual(['agent', 'llama', 'kiwix', 'whisper', 'sync', 'drill']);
    const lines = screen.getByLabelText('Lines') as HTMLSelectElement;
    expect(lines).toHaveValue('200');
    expect(Array.from(lines.options).map((option) => option.value)).toEqual(['50', '200', '500']);
  });

  it('a viewer gets no Fetch button and a short explanation', async () => {
    renderPage(detailFixture(), VIEWER);
    await openTab('Logs');
    expect(await screen.findByText(/needs operator access/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Fetch' })).toBeNull();
  });
});

describe('Keys tab', () => {
  it('shows the key hint, when it was reported and the credential prefix; no key material anywhere', async () => {
    const { container } = renderPage();
    await openTab('Keys');
    expect(await screen.findByTestId('key-hint')).toHaveTextContent('ab12…9f0e');
    expect(screen.getByTestId('credential-prefix')).toHaveTextContent('nod_Xy7Kq2…');
    expect(container.textContent).not.toMatch(/[0-9a-f]{32,}/i);
  });

  it('says so when the cloud has no key yet, with the fix of LAI-CLD-026', async () => {
    renderPage(detailFixture({ keys: { ...detailFixture().keys, keyHint: null, keyReportedAt: null } }));
    await openTab('Keys');
    expect(await screen.findByTestId('key-missing')).toHaveTextContent('Press "Refresh status" on the node');
  });

  it('Rotate key… lists what changes and queues key.rotate', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.key.rotate', params: {} }));
    renderPage();
    await openTab('Keys');
    fireEvent.click(await screen.findByRole('button', { name: 'Rotate key…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Rotate the key of ai-n2?' });
    expect(dialog).toHaveTextContent('Open WebUI uses the gateway, so usually nothing needs changing.');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Rotate key' }));
    await waitFor(() => expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'key.rotate', {}));
  });

  it('Replace credential… shows the new token once with the re-enroll steps, and never again', async () => {
    const token = `nod_${'z'.repeat(8)}${Date.now().toString(36)}`;
    vi.mocked(replaceNodeCredential).mockResolvedValue({
      nodeId: 'ai-n2',
      token,
      tokenPrefix: 'zzzzzz',
      credentialId: '11111111-1111-4111-8111-111111111111',
      createdAt: '2026-11-03T12:00:00.000Z',
      reenroll: ['On the node, run: sudo aictl cloud enroll', 'Paste the new credential when it asks, then press Enter'],
    });
    const first = renderPage();
    await openTab('Keys');
    fireEvent.click(await screen.findByRole('button', { name: 'Replace credential…' }));
    fireEvent.click(within(await screen.findByRole('dialog', { name: 'Replace the credential of ai-n2?' })).getByRole('button', { name: 'Replace credential' }));
    const shown = await screen.findByTestId('new-credential');
    expect(shown).toHaveTextContent(token);
    expect(screen.getByRole('dialog')).toHaveTextContent('sudo aictl cloud enroll');
    fireEvent.click(screen.getByRole('button', { name: 'I have copied it' }));
    await waitFor(() => expect(screen.queryByTestId('new-credential')).toBeNull());
    expect(document.body.textContent).not.toContain(token);

    // A reload (a fresh render of the page) cannot show it either: the API never returns it again.
    first.unmount();
    renderPage();
    await openTab('Keys');
    await screen.findByTestId('credential-prefix');
    expect(document.body.textContent).not.toContain(token);
    expect(replaceNodeCredential).toHaveBeenCalledTimes(1);
  });
});

describe('Settings tab', () => {
  it('masks the tailnet IP, and the address is absent from the page until Reveal; Reveal reads it once; Hide drops it', async () => {
    vi.mocked(revealTailnetIp).mockResolvedValue(FAKE_TAILNET_IP);
    renderPage();
    await openTab('Settings');
    expect(await screen.findByTestId('tailnet-ip')).toHaveTextContent('100.•••.•••.•••');
    expect(document.body.textContent).not.toContain(FAKE_TAILNET_IP);
    expect(revealTailnetIp).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Reveal' }));
    await waitFor(() => expect(screen.getByTestId('tailnet-ip')).toHaveTextContent(FAKE_TAILNET_IP));
    expect(revealTailnetIp).toHaveBeenCalledTimes(1);
    expect(revealTailnetIp).toHaveBeenCalledWith('ai-n2');
    fireEvent.click(screen.getByRole('button', { name: 'Hide' }));
    expect(document.body.textContent).not.toContain(FAKE_TAILNET_IP);
    // Leaving the tab drops a revealed address too.
    fireEvent.click(screen.getByRole('button', { name: 'Reveal' }));
    await waitFor(() => expect(document.body.textContent).toContain(FAKE_TAILNET_IP));
    await openTab('Models');
    expect(document.body.textContent).not.toContain(FAKE_TAILNET_IP);
  });

  it('a contributor and a viewer see the values read-only, without the tailnet IP section or any button', async () => {
    for (const held of [CONTRIBUTOR, VIEWER]) {
      const { unmount } = renderPage(detailFixture(), held);
      await openTab('Settings');
      expect(await screen.findByText('ai-n2.example.test')).toBeInTheDocument();
      expect(screen.queryByText(/Tailnet IP/)).toBeNull();
      expect(screen.queryByRole('button', { name: /retire|reveal|save/i })).toBeNull();
      expect(document.body.textContent).not.toContain('100.');
      unmount();
    }
  });

  it('an admin saves only the fields that changed through PATCH and sees the validation message of a refusal', async () => {
    vi.mocked(patchNode).mockRejectedValueOnce(new ApiError('The request is not valid.', 400, undefined, { issues: [{ path: 'publicHost', code: 'invalid_format', message: 'Enter a host name such as ai-n2.example.test' }] }));
    vi.mocked(patchNode).mockResolvedValue(detailFixture());
    renderPage();
    await openTab('Settings');
    expect(await screen.findByRole('button', { name: 'Save changes' })).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Public host'), { target: { value: 'not a host' } });
    fireEvent.change(screen.getByLabelText('Gateway priority'), { target: { value: '7' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    expect(await screen.findByText('Enter a host name such as ai-n2.example.test')).toBeInTheDocument();
    expect(patchNode).toHaveBeenLastCalledWith('ai-n2', { publicHost: 'not a host', gatewayPriority: 7 });
    fireEvent.change(screen.getByLabelText('Public host'), { target: { value: 'ai-n2.example.test' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(patchNode).toHaveBeenLastCalledWith('ai-n2', { gatewayPriority: 7 }));
    expect(await screen.findByText('Saved.')).toBeInTheDocument();
  });

  it('Retire node… needs the exact node id; a mismatch keeps the button disabled; success returns to the lab', async () => {
    vi.mocked(retireNode).mockResolvedValue({ nodeId: 'ai-n2', phase: 'retired', cancelledCommands: 1 });
    renderPage();
    await openTab('Settings');
    fireEvent.click(await screen.findByRole('button', { name: 'Retire node…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Retire ai-n2?' });
    expect(dialog).toHaveTextContent('the id is never reused');
    const confirm = within(dialog).getByRole('button', { name: 'Retire node' });
    expect(confirm).toBeDisabled();
    const input = within(dialog).getByLabelText('Type ai-n2 to confirm');
    for (const wrong of ['ai-n3', 'AI-N2', 'ai-n2 ', 'ai-n']) {
      fireEvent.change(input, { target: { value: wrong } });
      expect(confirm).toBeDisabled();
    }
    fireEvent.change(input, { target: { value: 'ai-n2' } });
    fireEvent.click(confirm);
    await waitFor(() => expect(retireNode).toHaveBeenCalledWith('ai-n2', 'ai-n2'));
    expect(await screen.findByText('The lab page')).toBeInTheDocument();
  });
});

describe('permissions: who sees which buttons', () => {
  const TABS = ['Models', 'Ark', 'Services', 'Logs', 'Keys', 'Settings'];

  it('a viewer sees no action button on any tab, and no header action', async () => {
    renderPage(detailFixture({ agentLatestVersion: '0.4.2' }), VIEWER);
    await screen.findByRole('heading', { level: 1 });
    for (const tab of TABS) {
      await openTab(tab);
      await screen.findByRole('tabpanel');
      expect(actionButtons().map((button) => button.textContent || button.getAttribute('aria-label'))).toEqual([]);
    }
  });

  it('a contributor sees the operate actions but none of the admin ones', async () => {
    renderPage(detailFixture({ agentLatestVersion: '0.4.2' }), CONTRIBUTOR);
    expect(await screen.findByRole('button', { name: 'Refresh status' })).toBeInTheDocument();
    for (const name of [/Go offline/, /Update agent/]) expect(screen.queryByRole('button', { name })).toBeNull();
    expect(within(screen.getByTestId(`model-row-${MODEL_READY}`)).getByRole('button', { name: 'Load' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /More actions/ })).toBeNull();
    await openTab('Keys');
    await screen.findByTestId('key-hint');
    expect(screen.queryByRole('button', { name: /Rotate key|Replace credential/ })).toBeNull();
    await openTab('Settings');
    await screen.findByText('ai-n2.example.test');
    expect(screen.queryByRole('button', { name: /Retire/ })).toBeNull();
  });

  it('an admin sees the admin actions', async () => {
    renderPage(detailFixture({ agentLatestVersion: '0.4.2' }), ADMIN);
    for (const name of [/Go offline/, /Update agent/, /Refresh status/]) expect(await screen.findByRole('button', { name })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /More actions/ }).length).toBeGreaterThan(0);
  });
});

describe('loading, error and empty states', () => {
  it('shows a spinner while loading', async () => {
    permissions = ADMIN;
    vi.mocked(getNodeDetail).mockReturnValue(new Promise(() => undefined));
    render(
      <MemoryRouter initialEntries={['/lab/nodes/ai-n2']}>
        <Routes>
          <Route path="/lab/nodes/:nodeId" element={<NodeDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByLabelText('Loading the node')).toBeInTheDocument();
  });

  it('shows "Could not load the node" with Try again on a failure, and recovers', async () => {
    permissions = ADMIN;
    vi.mocked(getNodeDetail).mockRejectedValueOnce(new ApiError('boom', 500));
    vi.mocked(getNodeDetail).mockResolvedValue(detailFixture());
    render(
      <MemoryRouter initialEntries={['/lab/nodes/ai-n2']}>
        <Routes>
          <Route path="/lab/nodes/:nodeId" element={<NodeDetailPage now={LAB_NOW} />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText('Could not load the node.')).toBeInTheDocument();
    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Try again' })));
    expect(await screen.findByRole('heading', { level: 1, name: /ai-n2/ })).toBeInTheDocument();
  });

  it('says there is no such node for a 404 (an unknown or retired id)', async () => {
    permissions = ADMIN;
    vi.mocked(getNodeDetail).mockRejectedValue(new ApiError('No Local AI node has this id', 404));
    render(
      <MemoryRouter initialEntries={['/lab/nodes/ai-n9']}>
        <Routes>
          <Route path="/lab/nodes/:nodeId" element={<NodeDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText('There is no node ai-n9. It may have been retired.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to the lab' })).toHaveAttribute('href', '/lab');
  });

  it('keeps the last answer on screen and warns when a refresh fails', async () => {
    permissions = ADMIN;
    vi.mocked(getNodeDetail).mockResolvedValueOnce(detailFixture());
    vi.mocked(getNodeDetail).mockRejectedValue(new ApiError('boom', 500));
    render(
      <MemoryRouter initialEntries={['/lab/nodes/ai-n2']}>
        <Routes>
          <Route path="/lab/nodes/:nodeId" element={<NodeDetailPage now={LAB_NOW} />} />
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByRole('heading', { level: 1 });
    // Refresh through a command settling is the quickest way to trigger a second read here.
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.status.refresh', status: 'succeeded', params: {}, finishedAt: '2026-11-03T12:00:02.000Z' }));
    fireEvent.click(screen.getByRole('button', { name: 'Refresh status' }));
    expect(await screen.findByText('Could not refresh the node. Showing the last answer.')).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument();
  });

  it('opens the tab named in the address', async () => {
    renderPage(detailFixture(), ADMIN, '/lab/nodes/ai-n2?tab=services');
    expect(await screen.findByTestId('service-row-llama')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Services' })).toHaveAttribute('aria-selected', 'true');
  });
});

describe('accessibility (axe)', () => {
  const tabs = ['Models', 'Ark', 'Services', 'Logs', 'Keys', 'Settings'];
  for (const [label, held] of [['an admin', ADMIN], ['a viewer', VIEWER]] as const) {
    it(`has no axe violations on any tab for ${label}`, async () => {
      const { container } = renderPage(detailFixture({ agentLatestVersion: '0.4.2' }), held);
      await screen.findByRole('heading', { level: 1 });
      for (const tab of tabs) {
        await openTab(tab);
        await screen.findByRole('tabpanel');
        const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
        expect(result.violations.map((violation) => `${tab}: ${violation.id}: ${violation.help}`)).toEqual([]);
      }
    }, 60_000);
  }

  it('has no axe violations on an Offline node with a command line, and with a dialog open', async () => {
    vi.mocked(sendCommand).mockResolvedValue(commandFixture({ type: 'localai.status.refresh', params: {} }));
    vi.mocked(getCommand).mockResolvedValue(commandFixture({ type: 'localai.status.refresh', params: {} }));
    const { container } = renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Refresh status' }));
    await screen.findByTestId('command-line');
    let result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
    expect(result.violations.map((violation) => violation.id)).toEqual([]);
    fireEvent.click(screen.getByRole('button', { name: 'Go offline…' }));
    const dialog = await screen.findByRole('dialog');
    result = await axe.run(dialog, { rules: { 'color-contrast': { enabled: false } } });
    expect(result.violations.map((violation) => violation.id)).toEqual([]);
  }, 30_000);
});

