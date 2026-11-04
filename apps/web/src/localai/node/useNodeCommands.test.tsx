import { ApiError } from '@marinoscar/platform-web/core';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';

import { cancelCommand, getCommand, listCommands, sendCommand } from '../api/commands';
import { commandFixture } from './fixture';
import { NodeCommandsProvider, refusalText, useNodeCommands } from './useNodeCommands';

vi.mock('../api/commands', () => ({ sendCommand: vi.fn(), getCommand: vi.fn(), cancelCommand: vi.fn(), listCommands: vi.fn() }));

const wrapper = (props: { onSettled?: (c: unknown) => void; pollMs?: number } = {}) =>
  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <NodeCommandsProvider nodeId="ai-n2" pollMs={props.pollMs ?? 10} onSettled={props.onSettled}>
        {children}
      </NodeCommandsProvider>
    );
  };

beforeEach(() => {
  vi.mocked(sendCommand).mockReset();
  vi.mocked(getCommand).mockReset();
  vi.mocked(cancelCommand).mockReset();
  vi.mocked(listCommands).mockReset().mockResolvedValue({ items: [], page: 1, pageSize: 20, total: 0, totalPages: 0 });
});

describe('refusalText', () => {
  it('maps the refusals of spec 3.9 to plain language', () => {
    expect(refusalText(new ApiError('x', 422, undefined, { code: 'LAI-CLD-034', mode: 'offline' }), 'ai-n2')).toEqual({ code: 'LAI-CLD-034', message: 'This computer is in Offline mode. Only someone at it can switch it back.' });
    expect(refusalText(new ApiError('x', 422, undefined, { code: 'LAI-CLD-034', mode: 'survival' }), 'ai-n2').message).toContain('Survival mode');
    expect(refusalText(new ApiError('x', 409, undefined, { code: 'LAI-CLD-035' }), 'ai-n2').code).toBe('LAI-CLD-035');
    expect(refusalText(new ApiError('Forbidden', 403), 'ai-n2').message).toBe('You do not have permission to do this.');
    expect(refusalText(new ApiError('x', 422, undefined, { reason: 'model_not_on_node' }), 'ai-n3').message).toBe('This model is not on ai-n3. Download it first.');
    expect(refusalText(new ApiError('Params are wrong', 400), 'ai-n2').message).toBe('Params are wrong');
    expect(refusalText(new TypeError('network'), 'ai-n2').message).toContain('Could not reach the cloud');
  });
});

describe('NodeCommandsProvider', () => {
  it('tracks a sent command, polls it until it settles, and tells the page once', async () => {
    const queued = commandFixture({ status: 'queued' });
    vi.mocked(sendCommand).mockResolvedValue(queued);
    vi.mocked(getCommand).mockResolvedValueOnce({ ...queued, status: 'running' }).mockResolvedValue({ ...queued, status: 'succeeded', finishedAt: '2026-11-03T12:00:09.000Z' });
    const onSettled = vi.fn();
    const { result } = renderHook(() => useNodeCommands(), { wrapper: wrapper({ onSettled }) });
    let outcome: Awaited<ReturnType<typeof result.current.send>> | undefined;
    await act(async () => { outcome = await result.current.send('model.load', { model_id: 'abc-model' }); });
    expect(outcome?.ok).toBe(true);
    expect(sendCommand).toHaveBeenCalledWith('ai-n2', 'model.load', { model_id: 'abc-model' });
    expect(result.current.commands[0]!.status).toBe('queued');
    await waitFor(() => expect(result.current.commands[0]!.status).toBe('succeeded'));
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(onSettled).toHaveBeenCalledTimes(1);
    // Settled: no more polling.
    const calls = vi.mocked(getCommand).mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(vi.mocked(getCommand).mock.calls.length).toBe(calls);
  });

  it('a refusal becomes a notice unless the caller asked for silence', async () => {
    vi.mocked(sendCommand).mockRejectedValue(new ApiError('x', 422, undefined, { code: 'LAI-CLD-034', mode: 'offline' }));
    const { result } = renderHook(() => useNodeCommands(), { wrapper: wrapper() });
    await act(async () => { await result.current.send('model.load', {}, { silent: true }); });
    expect(result.current.notice).toBeNull();
    await act(async () => { await result.current.send('model.load', {}); });
    expect(result.current.notice).toContain('Offline mode');
    expect(result.current.notice).toContain('LAI-CLD-034');
    act(() => result.current.clearNotice());
    expect(result.current.notice).toBeNull();
  });

  it('adopts only the active commands that were already there, and finds them by type and subject', async () => {
    vi.mocked(listCommands).mockResolvedValue({
      items: [commandFixture({ status: 'running', params: { model_id: 'abc-model' } }), commandFixture({ status: 'succeeded', finishedAt: '2026-11-03T12:00:01.000Z' })],
      page: 1, pageSize: 20, total: 2, totalPages: 1,
    });
    vi.mocked(getCommand).mockImplementation(async () => commandFixture({ status: 'running', params: { model_id: 'abc-model' } }));
    const { result } = renderHook(() => useNodeCommands(), { wrapper: wrapper({ pollMs: 100_000 }) });
    await waitFor(() => expect(result.current.commands).toHaveLength(1));
    expect(result.current.activeFor('model.load', 'abc-model')).toBeDefined();
    expect(result.current.activeFor('localai.model.load')).toBeDefined();
    expect(result.current.activeFor('model.load', 'other-model')).toBeUndefined();
    expect(result.current.activeFor('model.unload')).toBeUndefined();
  });

  it('dismiss removes a settled line', async () => {
    const done = commandFixture({ status: 'succeeded', finishedAt: '2026-11-03T12:00:01.000Z' });
    vi.mocked(sendCommand).mockResolvedValue(done);
    const { result } = renderHook(() => useNodeCommands(), { wrapper: wrapper() });
    await act(async () => { await result.current.send('status.refresh'); });
    expect(result.current.commands).toHaveLength(1);
    act(() => result.current.dismiss(done.id));
    expect(result.current.commands).toHaveLength(0);
  });
});
