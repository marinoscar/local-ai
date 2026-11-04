// The Add-node wizard: the three steps, the show-once credential (masked, copied from the prop, cleared when the operator
// continues, never in storage, the URL or history), the live Enrolled / Key received indicators, the publish checklist,
// permission gating and the axe checks. The transport and the permissions are stubbed; nothing here touches a network.
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import axe from 'axe-core';
import { MemoryRouter } from 'react-router-dom';

import { AddNodeWizard } from './AddNodeWizard';
import { ALL_DONE, checklist, createResult, fakeClient, makeToken, NODE_ID, SHA } from './fixture';
import { PUBLISH_AUTOMATICALLY_TOOLTIP } from './StepPublish';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

const apiError = (message: string, reason: string, status = 409) => Object.assign(new Error(message), { status, details: { reason } });

function renderWizard(client = fakeClient(), url = '/lab/nodes/new') {
  return { client, ...render(<MemoryRouter initialEntries={[url]}><AddNodeWizard client={client} /></MemoryRouter>) };
}

async function createOnStepOne(token = makeToken(), client = fakeClient({ createNode: vi.fn().mockImplementation(async () => createResult(token)) })) {
  const view = renderWizard(client);
  await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
  fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
  await screen.findByRole('heading', { name: /Node credential/ });
  return { ...view, token };
}

const noViolations = async (container: HTMLElement) => {
  const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
  expect(result.violations.map((violation) => `${violation.id}: ${violation.help}`)).toEqual([]);
};

beforeEach(() => {
  permissions = ['lab:read', 'lab:admin'];
  localStorage.clear();
  sessionStorage.clear();
});

describe('permissions', () => {
  it('shows a holder of lab:read only (no lab:admin) no form and calls nothing', () => {
    permissions = ['lab:read'];
    const { client } = renderWizard();
    expect(screen.getByTestId('not-allowed')).toHaveTextContent('Only administrators can add a node.');
    expect(screen.queryByRole('button', { name: 'Create node' })).toBeNull();
    expect(client.nextNodeId).not.toHaveBeenCalled();
  });
});

describe('step 1: describe the node', () => {
  it('is Step 1 of 3, suggests the next free id, derives the public host, and defaults to Pattern B', async () => {
    renderWizard();
    expect(screen.getByText(/Step 1 of 3/)).toHaveTextContent('Describe the node');
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue('ai-n4'));
    expect(screen.getByLabelText('Public host')).toHaveValue('ai-n4.marin.cr');
    expect(screen.getByRole('radio', { name: /Pattern B/ })).toBeChecked();
    expect(screen.getByRole('radio', { name: /Pattern A/ })).not.toBeChecked();
    expect(screen.getByRole('button', { name: 'Create node' })).toBeEnabled();
  });

  it('does not overwrite an id the operator already typed when the suggestion arrives late', async () => {
    let resolve!: (id: string) => void;
    const client = fakeClient({ nextNodeId: vi.fn().mockReturnValue(new Promise<string>((r) => (resolve = r))) });
    renderWizard(client);
    fireEvent.change(screen.getByLabelText(/^Node id/), { target: { value: 'ai-n9' } });
    await act(async () => resolve('ai-n4'));
    expect(screen.getByLabelText(/^Node id/)).toHaveValue('ai-n9');
  });

  it.each(['ai-n0', 'ai-n04', 'ai-n10000', 'localai', 'ai-n'])('refuses the id %j before calling the server', async (bad) => {
    const { client } = renderWizard();
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    fireEvent.change(screen.getByLabelText(/^Node id/), { target: { value: bad } });
    expect(screen.getByText(/Use ai-n and a number from 1 to 9999/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create node' })).toBeDisabled();
    expect(client.createNode).not.toHaveBeenCalled();
  });

  it('refuses an address outside the tailnet range', async () => {
    renderWizard();
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    fireEvent.change(screen.getByLabelText(/^Tailnet IP/), { target: { value: ['192', '168', '1', '5'].join('.') } });
    expect(screen.getByText(/not a Tailscale address/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create node' })).toBeDisabled();
  });

  it('sends only what was filled in; blank optional fields and the derived host are left out', async () => {
    const { client } = renderWizard();
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    await screen.findByRole('heading', { name: /Node credential/ });
    expect(client.createNode).toHaveBeenCalledWith({ nodeId: 'ai-n4', servePattern: 'B' });
  });

  it('sends the fields it was given, a host override and Pattern A included', async () => {
    const { client } = renderWizard();
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    const tailnetIp = ['100', '71', '8', '9'].join('.');
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Dell Precision (ai-n4)' } });
    fireEvent.change(screen.getByLabelText('Public host'), { target: { value: 'node.example.test' } });
    fireEvent.click(screen.getByRole('radio', { name: /Pattern A/ }));
    fireEvent.change(screen.getByLabelText(/^Tailnet name/), { target: { value: 'dell-linux' } });
    fireEvent.change(screen.getByLabelText(/^Tailnet IP/), { target: { value: tailnetIp } });
    fireEvent.mouseDown(screen.getByLabelText(/^Profile hint/));
    fireEvent.click(await screen.findByRole('option', { name: 'cuda-12gb' }));
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    await screen.findByRole('heading', { name: /Node credential/ });
    expect(client.createNode).toHaveBeenCalledWith({
      nodeId: 'ai-n4',
      displayName: 'Dell Precision (ai-n4)',
      publicHost: 'node.example.test',
      servePattern: 'A',
      tailnetMachine: 'dell-linux',
      tailnetIp,
      profile: 'cuda-12gb',
    });
  });

  it('says an id was used before, and offers the next free one', async () => {
    const client = fakeClient({
      createNode: vi.fn().mockRejectedValue(apiError('This node id has been used before.', 'node_id_used')),
      nextNodeId: vi.fn().mockResolvedValueOnce('ai-n4').mockResolvedValue('ai-n5'),
    });
    renderWizard(client);
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue('ai-n4'));
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    expect(await screen.findByText(/That id has been used before/)).toBeInTheDocument();
    fireEvent.click(await screen.findByRole('button', { name: 'Use ai-n5' }));
    expect(screen.getByLabelText(/^Node id/)).toHaveValue('ai-n5');
    expect(screen.queryByText(/That id has been used before/)).toBeNull();
  });

  it('shows another failure as an error and lets the operator try again', async () => {
    const client = fakeClient({ createNode: vi.fn().mockRejectedValueOnce(new Error('The request is not valid.')).mockImplementation(async () => createResult()) });
    renderWizard(client);
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('The request is not valid.');
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    await screen.findByRole('heading', { name: /Node credential/ });
  });

  it('has no axe violations', async () => {
    const { container } = renderWizard();
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    await noViolations(container);
  });
});

describe('step 2: install, with the credential shown once', () => {
  it('shows the credential masked, reveals it on request, and copies the full value from the prop', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    const { token } = await createOnStepOne();
    expect(screen.getByText(/Step 2 of 3/)).toHaveTextContent('Install on the node');
    // Masked: the prefix and dots, never the secret part.
    const box = screen.getByTestId('credential');
    expect(box).toHaveTextContent(token.slice(0, 8));
    expect(document.body.textContent).not.toContain(token);
    expect(screen.getByText(/only time the credential is shown/)).toBeInTheDocument();
    expect(screen.getByText(/Expires: never/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Copy node credential' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(token));
    expect(document.body.textContent).not.toContain(token); // still masked after copying

    fireEvent.click(screen.getByRole('button', { name: 'Show' }));
    expect(box).toHaveTextContent(token);
    fireEvent.click(screen.getByRole('button', { name: 'Hide' }));
    expect(document.body.textContent).not.toContain(token);
  });

  it('shows the three install commands with a copy button, and the checksum to compare', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    await createOnStepOne();
    const commands = screen.getByLabelText('Install commands');
    expect(commands).toHaveTextContent('curl -fsSLO https://github.com/marinoscar/local-ai/releases/download/node-v0.4.1/install-node.sh');
    expect(commands).toHaveTextContent(`sudo bash install-node.sh --node-id ${NODE_ID} --cloud-url https://ai.marin.cr --enroll`);
    expect(commands.textContent).not.toMatch(/nod_/);
    expect(commands.textContent).not.toMatch(/\|\s*(sudo\s+)?(ba)?sh/); // never piped into a shell
    expect(screen.getByTestId('script-sha256')).toHaveTextContent(SHA);
    fireEvent.click(screen.getByRole('button', { name: 'Copy install commands' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(expect.stringContaining('--enroll')));
    expect(screen.getByText(/It asks for the credential above/)).toBeInTheDocument();
  });

  it('says the checksum is unknown and links the release page when the server could not read it', async () => {
    const client = fakeClient({ createNode: vi.fn().mockResolvedValue(createResult(makeToken(), { scriptSha256: null, note: 'Checksum unknown, check the release page.' })) });
    renderWizard(client);
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    await screen.findByLabelText('Install commands');
    expect(screen.getByText(/unknown, check the release page/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'release page' })).toHaveAttribute('href', 'https://github.com/marinoscar/local-ai/releases/tag/node-v0.4.1');
    expect(screen.queryByTestId('script-sha256')).toBeNull();
  });

  it('shows the reason, not commands, while no agent release is configured', async () => {
    const note = 'No agent release is set yet.';
    const client = fakeClient({ createNode: vi.fn().mockResolvedValue(createResult(makeToken(), { oneLiner: null, pinnedScriptUrl: null, scriptSha256: null, note, releasePageUrl: null })) });
    renderWizard(client);
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    expect(await screen.findByText(note)).toBeInTheDocument();
    expect(screen.queryByLabelText('Install commands')).toBeNull();
  });

  it('turns Enrolled and Key received green by itself as the node contacts the cloud', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const nodeStatus = vi
        .fn()
        .mockResolvedValueOnce({ nodeId: NODE_ID, phase: 'pending', keyReceived: false })
        .mockResolvedValueOnce({ nodeId: NODE_ID, phase: 'enrolled', keyReceived: false })
        .mockResolvedValue({ nodeId: NODE_ID, phase: 'enrolled', keyReceived: true });
      renderWizard(fakeClient({ nodeStatus }));
      await vi.waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
      fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
      await screen.findByRole('heading', { name: /Waiting for ai-n4/ });
      expect(screen.getByText(/Waiting for ai-n4 to contact the cloud/)).toBeInTheDocument();
      expect(screen.queryByText(/Enrolled/)).toBeNull();

      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      expect(await screen.findByText(/✔ Enrolled/)).toBeInTheDocument();
      expect(screen.getByText(/Waiting for the node to send its key/)).toBeInTheDocument();

      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      expect(await screen.findByText(/✔ Key received/)).toBeInTheDocument();
      // Polled every 3 s, and stopped once both are green.
      const calls = nodeStatus.mock.calls.length;
      await act(async () => { await vi.advanceTimersByTimeAsync(9000); });
      expect(nodeStatus.mock.calls.length).toBe(calls);
      expect(nodeStatus).toHaveBeenCalledWith(NODE_ID);
    } finally {
      vi.useRealTimers();
    }
  });

  it('Continue waits for "I have saved it" (or for the node to enrol), then removes the credential from the page', async () => {
    const { token } = await createOnStepOne();
    const next = screen.getByRole('button', { name: 'Continue to publish' });
    expect(next).toBeDisabled();
    fireEvent.click(screen.getByRole('checkbox', { name: /I have copied the credential/ }));
    expect(next).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: 'Show' }));
    expect(document.body.textContent).toContain(token);

    fireEvent.click(next);
    await screen.findByRole('list', { name: 'Publish checklist' });
    expect(screen.getByText(/Step 3 of 3/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(token);
    expect(document.body.innerHTML).not.toContain(token);
  });

  it('after the node enrolled, Continue needs no confirmation', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      renderWizard(fakeClient({ nodeStatus: vi.fn().mockResolvedValue({ nodeId: NODE_ID, phase: 'enrolled', keyReceived: true }) }));
      await vi.waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
      fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
      await screen.findByRole('heading', { name: /Waiting for ai-n4/ });
      await vi.waitFor(() => expect(screen.getByRole('button', { name: 'Continue to publish' })).toBeEnabled());
      expect(screen.queryByRole('checkbox')).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps the credential out of storage, the URL and history, in every step', async () => {
    const { token } = await createOnStepOne();
    const where = () => [JSON.stringify({ ...localStorage }), JSON.stringify({ ...sessionStorage }), window.location.href, JSON.stringify(window.history.state), document.title, document.cookie].join('\n');
    expect(where()).not.toContain(token);
    fireEvent.click(screen.getByRole('checkbox', { name: /I have copied the credential/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Continue to publish' }));
    await screen.findByRole('list', { name: 'Publish checklist' });
    expect(where()).not.toContain(token);
  });

  it('asks before the tab closes while the only copy is on screen and unused, and stops asking once it is gone', async () => {
    const added: string[] = [];
    const original = window.addEventListener.bind(window);
    const add = vi.spyOn(window, 'addEventListener').mockImplementation(((type: string, ...rest: unknown[]) => {
      added.push(type);
      return (original as (...args: unknown[]) => void)(type, ...rest);
    }) as never);
    const { token } = await createOnStepOne();
    expect(token).toBeTruthy();
    expect(added).toContain('beforeunload');
    add.mockRestore();

    // A real listener check: the handler cancels the unload.
    const event = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);

    fireEvent.click(screen.getByRole('checkbox', { name: /I have copied the credential/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Continue to publish' }));
    await screen.findByRole('list', { name: 'Publish checklist' });
    const after = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(after);
    expect(after.defaultPrevented).toBe(false);
  });

  it('has no axe violations', async () => {
    const { container } = await createOnStepOne();
    await noViolations(container);
  });
});

describe('step 3: publish', () => {
  async function toPublish(client = fakeClient()) {
    const view = renderWizard(client);
    await waitFor(() => expect(screen.getByLabelText(/^Node id/)).toHaveValue(NODE_ID));
    fireEvent.click(screen.getByRole('button', { name: 'Create node' }));
    await screen.findByRole('heading', { name: /Node credential/ });
    fireEvent.click(screen.getByRole('checkbox', { name: /I have copied the credential/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Continue to publish' }));
    await screen.findByRole('list', { name: 'Publish checklist' });
    return view;
  }

  it('lists the eight steps in order with a word and an icon for each state, and the automatic ones marked', async () => {
    await toPublish(fakeClient({ checklist: vi.fn().mockResolvedValue(checklist({ tailnet: 'done', enrolled: 'done', key: 'done', dns: 'failed' }, { dns: 'No A record for ai-n4.marin.cr yet.' })) }));
    const rows = within(screen.getByRole('list', { name: 'Publish checklist' })).getAllByRole('listitem');
    expect(rows).toHaveLength(8);
    expect(rows.map((row) => row.getAttribute('data-testid'))).toEqual(['step-tailnet', 'step-dns', 'step-bootstrap_vhost', 'step-certificate', 'step-https_vhost', 'step-enrolled', 'step-key', 'step-gateway_test']);
    expect(within(rows[0]!).getByText('Done')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('Failed')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('No A record for ai-n4.marin.cr yet.')).toBeInTheDocument();
    expect(within(rows[2]!).getByText('To do')).toBeInTheDocument();
    expect(within(rows[5]!).getByText('(automatic)')).toBeInTheDocument();
    expect(within(rows[5]!).queryByRole('button')).toBeNull();
    expect(within(rows[6]!).queryByRole('button')).toBeNull();
    // Manual steps are marked, check steps are checked.
    expect(within(rows[0]!).getByRole('button', { name: 'Mark step 1 as to do' })).toBeInTheDocument();
    expect(within(rows[2]!).getByRole('button', { name: 'Mark step 3 done' })).toBeInTheDocument();
    expect(within(rows[1]!).getByRole('button', { name: 'Check' })).toBeInTheDocument();
    expect(within(rows[7]!).getByRole('button', { name: 'Check' })).toBeInTheDocument();
    expect(screen.queryByTestId('lab-banner')).toBeNull();
  });

  it('has the Publish automatically button, disabled, with the tooltip', async () => {
    await toPublish();
    const button = screen.getByRole('button', { name: 'Publish automatically' });
    expect(button).toBeDisabled();
    fireEvent.mouseOver(button.parentElement!);
    expect(await screen.findByRole('tooltip')).toHaveTextContent(PUBLISH_AUTOMATICALLY_TOOLTIP);
    expect(PUBLISH_AUTOMATICALLY_TOOLTIP).toBe('Coming later: needs the VPS helper (§4.10)');
  });

  it('Check runs the server check and shows the answer; Mark done / Mark as to do call the server', async () => {
    const act_ = vi
      .fn()
      .mockResolvedValueOnce(checklist({ dns: 'done' }, { dns: 'ai-n4.marin.cr points to the same address as ai.marin.cr.' }))
      .mockResolvedValueOnce(checklist({ tailnet: 'done' }))
      .mockResolvedValueOnce(checklist());
    const client = fakeClient({ act: act_ });
    await toPublish(client);
    const dns = screen.getByTestId('step-dns');
    fireEvent.click(within(dns).getByRole('button', { name: 'Check' }));
    expect(await within(await screen.findByTestId('step-dns')).findByText(/points to the same address/)).toBeInTheDocument();
    expect(act_).toHaveBeenLastCalledWith(NODE_ID, 'dns', 'check');
    expect(within(screen.getByTestId('step-dns')).getByText('Done')).toBeInTheDocument();

    fireEvent.click(within(screen.getByTestId('step-tailnet')).getByRole('button', { name: 'Mark step 1 done' }));
    await waitFor(() => expect(within(screen.getByTestId('step-tailnet')).getByText('Done')).toBeInTheDocument());
    expect(act_).toHaveBeenLastCalledWith(NODE_ID, 'tailnet', 'mark_done');
    fireEvent.click(within(screen.getByTestId('step-tailnet')).getByRole('button', { name: 'Mark step 1 as to do' }));
    await waitFor(() => expect(within(screen.getByTestId('step-tailnet')).getByText('To do')).toBeInTheDocument());
    expect(act_).toHaveBeenLastCalledWith(NODE_ID, 'tailnet', 'mark_todo');
  });

  it('shows a rejected action on its own row and keeps the rest', async () => {
    await toPublish(fakeClient({ act: vi.fn().mockRejectedValue(apiError('The dns step is completed with its Check button.', 'not_a_manual_step', 400)) }));
    fireEvent.click(within(screen.getByTestId('step-dns')).getByRole('button', { name: 'Check' }));
    expect(await within(screen.getByTestId('step-dns')).findByRole('alert')).toHaveTextContent('The dns step is completed with its Check button.');
    expect(within(screen.getByTestId('step-certificate')).queryByRole('alert')).toBeNull();
  });

  it('shows the rendered file or command on demand, copies it, and explains a placeholder', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    await toPublish();
    const row = screen.getByTestId('step-https_vhost');
    expect(within(row).queryByText(/server_name/)).toBeNull();
    fireEvent.click(within(row).getByRole('button', { name: 'Show file' }));
    expect(await within(row).findByLabelText('File /opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf')).toHaveTextContent('server_name ai-n4.marin.cr;');
    expect(within(row).getByText(/tailnet IP is not stored yet/)).toBeInTheDocument();
    fireEvent.click(within(row).getByRole('button', { name: 'Copy Final HTTPS vhost (Pattern B)' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(expect.stringContaining('server_name ai-n4.marin.cr;')));
    fireEvent.click(within(row).getByRole('button', { name: 'Hide' }));
    await waitFor(() => expect(within(row).queryByLabelText(/^File /)).toBeNull());
    expect(within(screen.getByTestId('step-dns')).getByRole('button', { name: 'Show command' })).toBeInTheDocument();
  });

  it('shows "ai-n4 is part of the lab" when all eight are done', async () => {
    await toPublish(fakeClient({ checklist: vi.fn().mockResolvedValue(checklist(ALL_DONE)) }));
    expect(await screen.findByTestId('lab-banner')).toHaveTextContent('ai-n4 is part of the lab');
  });

  it('the automatic steps turn green while the page is open (the checklist is polled)', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const read = vi.fn().mockResolvedValueOnce(checklist()).mockResolvedValue(checklist({ enrolled: 'done', key: 'done' }));
      renderWizard(fakeClient({ checklist: read }), `/lab/nodes/new?node=${NODE_ID}`);
      await vi.waitFor(() => expect(within(screen.getByTestId('step-enrolled')).getByText('To do')).toBeInTheDocument());
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
      await vi.waitFor(() => expect(within(screen.getByTestId('step-enrolled')).getByText('Done')).toBeInTheDocument());
      expect(within(screen.getByTestId('step-key')).getByText('Done')).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it('opens straight on the publish step for ?node=ai-n4 (a node that already exists), and ignores a malformed one', async () => {
    const { client, unmount } = renderWizard(fakeClient(), `/lab/nodes/new?node=${NODE_ID}`);
    await screen.findByRole('list', { name: 'Publish checklist' });
    expect(client.checklist).toHaveBeenCalledWith(NODE_ID);
    expect(client.nextNodeId).not.toHaveBeenCalled();
    unmount();
    renderWizard(fakeClient(), '/lab/nodes/new?node=../../etc');
    expect(await screen.findByRole('button', { name: 'Create node' })).toBeInTheDocument();
  });

  it('shows a load failure with a way to retry', async () => {
    const read = vi.fn().mockRejectedValueOnce(new Error('network')).mockResolvedValue(checklist());
    renderWizard(fakeClient({ checklist: read }), `/lab/nodes/new?node=${NODE_ID}`);
    expect(await screen.findByText('Could not load the checklist.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await screen.findByRole('list', { name: 'Publish checklist' });
  });

  it('has no axe violations, with the details and a file open', async () => {
    const { container } = await toPublish(fakeClient({ checklist: vi.fn().mockResolvedValue(checklist({ tailnet: 'done', dns: 'failed' }, { dns: 'No A record yet.' })) }));
    fireEvent.click(within(screen.getByTestId('step-https_vhost')).getByRole('button', { name: 'Show file' }));
    await screen.findByLabelText(/^File /);
    await noViolations(container);
  });
});
