// The alerts hook: loads once, polls every 5 s only while the page is visible, refreshes at once on a filter change and
// when the tab returns, keeps the last answer on a failed refresh, ignores a stale answer, and acknowledges then reloads.
import { act, renderHook } from '@testing-library/react';

import { alertsFixture } from './fixture';
import { ALERTS_POLL_MS, defaultFetchAlerts, useAlerts } from './useAlerts';

vi.mock('../../api', () => ({ api: { get: vi.fn(async () => ({ alerts: [] })), post: vi.fn() } }));

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

const ACTIVE = { state: 'active', nodeId: '' } as const;

describe('useAlerts', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('polls every 5 s while the page is visible', async () => {
    const fetchAlerts = vi.fn().mockResolvedValue(alertsFixture());
    const { result } = renderHook(() => useAlerts(ACTIVE, fetchAlerts));
    expect(result.current.loading).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    expect(result.current.loading).toBe(false);
    expect(result.current.data?.alerts).toHaveLength(4);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ALERTS_POLL_MS);
    });
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
  });

  it('stops polling while the tab is hidden and refreshes at once when it returns', async () => {
    const fetchAlerts = vi.fn().mockResolvedValue(alertsFixture());
    renderHook(() => useAlerts(ACTIVE, fetchAlerts));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => setVisibility('hidden'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ALERTS_POLL_MS * 3);
    });
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    await act(async () => {
      setVisibility('visible');
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
  });

  it('loads again, with the new filters, when a filter changes', async () => {
    const fetchAlerts = vi.fn().mockResolvedValue(alertsFixture());
    const { rerender } = renderHook(({ filters }) => useAlerts(filters, fetchAlerts), { initialProps: { filters: { state: 'active', nodeId: '' } as const } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    rerender({ filters: { state: 'all', nodeId: 'ai-n2' } as never });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchAlerts).toHaveBeenLastCalledWith({ state: 'all', nodeId: 'ai-n2' });
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
  });

  it('keeps the last answer when a refresh fails, and clears the error on the next success', async () => {
    const fetchAlerts = vi.fn().mockResolvedValueOnce(alertsFixture()).mockRejectedValueOnce(new Error('down')).mockResolvedValue(alertsFixture());
    const { result } = renderHook(() => useAlerts(ACTIVE, fetchAlerts));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ALERTS_POLL_MS);
    });
    expect(result.current.error?.message).toBe('down');
    expect(result.current.data?.alerts).toHaveLength(4);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ALERTS_POLL_MS);
    });
    expect(result.current.error).toBeNull();
  });

  it('acknowledge posts then reloads, and a failed post rejects without reloading', async () => {
    const fetchAlerts = vi.fn().mockResolvedValue(alertsFixture());
    const acknowledgeAlert = vi.fn().mockResolvedValueOnce({}).mockRejectedValueOnce(new Error('409'));
    const { result } = renderHook(() => useAlerts(ACTIVE, fetchAlerts, acknowledgeAlert));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      await result.current.acknowledge('a-1');
    });
    expect(acknowledgeAlert).toHaveBeenCalledWith('a-1');
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
    await act(async () => {
      await expect(result.current.acknowledge('a-2')).rejects.toThrow('409');
    });
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
  });

  it('the default transport asks for the state and the node', async () => {
    const { api } = await import('../../api');
    await defaultFetchAlerts({ state: 'cleared', nodeId: 'ai-n2' });
    expect(api.get).toHaveBeenLastCalledWith('/localai/alerts?state=cleared&nodeId=ai-n2');
    await defaultFetchAlerts({ state: 'active', nodeId: '' });
    expect(api.get).toHaveBeenLastCalledWith('/localai/alerts?state=active');
  });
});
