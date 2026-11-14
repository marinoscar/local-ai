// The workload buttons of the node page over a mocked API (MSW, the real HTTP client; #514): what the node is set for,
// one button per workload of its snapshot (the active one pressed, one that does not fit disabled with the reason),
// a press queues the switch through its own route and the command line follows it, a refusal shows its doctor code,
// hidden in Offline mode and for a person without `localai_workloads:write`, an agent that reports no workloads, and
// Spanish.
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';

import { LanguageProvider, type Lang } from '../../i18n/LanguageProvider';
import type { NodeDetail } from '../api/types';
import { workloadCommand } from '../music/musicFixture';
import { CommandStatusLine } from './CommandStatusLine';
import { detailFixture } from './fixture';
import { NodeWorkloads } from './NodeWorkloads';
import { NodeCommandsProvider, useNodeCommands } from './useNodeCommands';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

const WORKLOADS = {
  active: 'coding',
  items: [
    { name: 'coding', label: { en: 'Coding', es: 'Programación' }, models: ['qwen3.6-35b-a3b-q4km'], fits: true, missing: [] },
    { name: 'music', label: { en: 'Music', es: 'Música' }, models: ['acestep-v15-turbo'], fits: true, missing: [] },
    { name: 'video', label: { en: 'Video', es: 'Video' }, models: ['qwen3.6-35b-a3b-q4km'], fits: false, missing: [] },
  ],
};

let activations: string[] = [];
let activateAnswer: () => Response = () => HttpResponse.json({ data: workloadCommand() }, { status: 201 });
let commandReads: unknown[] = [];
const server = setupServer(
  http.get('/api/localai/nodes/:nodeId/commands', () => HttpResponse.json({ data: { items: [], page: 1, pageSize: 20, total: 0, totalPages: 0 } })),
  http.post('/api/localai/nodes/:nodeId/workloads/:name/activate', ({ params }) => {
    activations.push(`${String(params.nodeId)}:${String(params.name)}`);
    return activateAnswer();
  }),
  http.get('/api/localai/commands/:id', () => HttpResponse.json({ data: commandReads.length > 1 ? commandReads.shift() : commandReads[0] })),
);
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterAll(() => server.close());
beforeEach(() => {
  permissions = ['lab:read', 'lab:operate', 'localai_workloads:write'];
  activations = [];
  commandReads = [];
  activateAnswer = () => HttpResponse.json({ data: workloadCommand() }, { status: 201 });
});
afterEach(cleanup);

function Lines() {
  const commands = useNodeCommands();
  return (
    <>
      {commands.notice ? <p role="alert">{commands.notice}</p> : null}
      <ul>
        {commands.commands.map((command) => (
          <CommandStatusLine key={command.id} command={command} canCancel={false} onCancel={() => undefined} onDismiss={commands.dismiss} />
        ))}
      </ul>
    </>
  );
}

function renderRow(node: NodeDetail = detailFixture({ lastState: { ...detailFixture().lastState!, workloads: WORKLOADS } }), lang: Lang = 'en') {
  return render(
    <LanguageProvider initial={lang}>
      <NodeCommandsProvider nodeId={node.nodeId} pollMs={20}>
        <NodeWorkloads node={node} />
        <Lines />
      </NodeCommandsProvider>
    </LanguageProvider>,
  );
}

describe('the workload buttons', () => {
  it('says what the node is set for and offers each workload, the active one pressed and the one that does not fit disabled', () => {
    renderRow();
    const row = screen.getByTestId('workload-switcher');
    expect(row).toHaveTextContent('This computer is set for: Coding');
    expect(within(row).getByRole('button', { name: 'Coding' })).toHaveAttribute('aria-pressed', 'true');
    expect(within(row).getByRole('button', { name: 'Music' })).toBeEnabled();
    expect(within(row).getByRole('button', { name: 'Video' })).toBeDisabled();
    expect(row).toHaveTextContent('Does not fit this graphics card.');
  });

  it('a press queues the switch on its own route and the command line follows it to Done', async () => {
    commandReads = [workloadCommand({ status: 'running' }), workloadCommand({ status: 'succeeded', result: { active: 'music' } })];
    renderRow();
    fireEvent.click(screen.getByRole('button', { name: 'Music' }));
    await waitFor(() => expect(activations).toEqual(['ai-n2:music']));
    expect(await screen.findByText('Switch to music')).toBeInTheDocument();
    expect(await screen.findByText('Done')).toBeInTheDocument();
  });

  it('a refusal shows its doctor code', async () => {
    activateAnswer = () => HttpResponse.json({ statusCode: 422, message: 'This computer has no workload with that name.', details: { code: 'LAI-LLM-050' } }, { status: 422 });
    renderRow();
    fireEvent.click(screen.getByRole('button', { name: 'Music' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('This computer has no workload with that name. (LAI-LLM-050)');
  });

  it('a failed switch shows the node\'s doctor code on the command line', async () => {
    commandReads = [workloadCommand({ status: 'failed', error: { code: 'LAI-LLM-052', message: 'Workload activation failed; previous models restored.' } })];
    renderRow();
    fireEvent.click(screen.getByRole('button', { name: 'Music' }));
    expect(await screen.findByText(/LAI-LLM-052/)).toBeInTheDocument();
  });

  it('in Offline mode there are no buttons, only the reason with its code', () => {
    renderRow(detailFixture({ mode: 'offline', lastState: { ...detailFixture().lastState!, workloads: WORKLOADS } }));
    expect(screen.queryByRole('button', { name: 'Music' })).toBeNull();
    expect(screen.getByTestId('workload-switcher')).toHaveTextContent('LAI-CLD-034');
  });

  it('without localai_workloads:write it only says what the node is set for', () => {
    permissions = ['lab:read'];
    renderRow();
    expect(screen.getByTestId('workload-switcher')).toHaveTextContent('This computer is set for: Coding');
    expect(screen.queryByRole('button', { name: 'Music' })).toBeNull();
  });

  it('an agent that reports no workloads gets a plain sentence', () => {
    renderRow(detailFixture());
    expect(screen.getByTestId('workloads-unknown')).toHaveTextContent('Update its agent');
  });

  it('speaks Spanish', () => {
    renderRow(undefined, 'es');
    expect(screen.getByTestId('workload-switcher')).toHaveTextContent('Esta computadora está configurada para: Programación');
    expect(screen.getByRole('button', { name: 'Música' })).toBeEnabled();
  });
});
