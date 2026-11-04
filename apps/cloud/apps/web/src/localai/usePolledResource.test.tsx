// Polls while the page is visible, stops when hidden, refreshes when it comes back, keeps the last answer on a failed
// refresh, and ignores a slow answer that a newer one overtook.
import { act, renderHook } from '@testing-library/react';

import { usePolledResource } from './usePolledResource';

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}
const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });

describe('usePolledResource', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
  });
  afterEach(() => vi.useRealTimers());

  it('loads once, then every interval while visible', async () => {
    const fetcher = vi.fn().mockResolvedValue('a');
    const { result } = renderHook(() => usePolledResource(fetcher, 5000));
    expect(result.current.loading).toBe(true);
    await tick(0);
    expect(result.current).toMatchObject({ data: 'a', loading: false, error: null });
    await tick(4999);
    expect(fetcher).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(fetcher).toHaveBeenCalledTimes(2);
    await tick(15_000);
    expect(fetcher).toHaveBeenCalledTimes(5);
  });

  it('stops while hidden and refreshes at once when visible again', async () => {
    const fetcher = vi.fn().mockResolvedValue('a');
    renderHook(() => usePolledResource(fetcher, 5000));
    await tick(0);
    act(() => setVisibility('hidden'));
    await tick(30_000);
    expect(fetcher).toHaveBeenCalledTimes(1);
    act(() => setVisibility('visible'));
    await tick(0);
    expect(fetcher).toHaveBeenCalledTimes(2);
    await tick(5000);
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it('keeps the last answer when a refresh fails and clears the error on the next success', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce('a').mockRejectedValueOnce(new Error('boom')).mockResolvedValue('b');
    const { result } = renderHook(() => usePolledResource(fetcher, 1000));
    await tick(0);
    await tick(1000);
    expect(result.current.data).toBe('a');
    expect(result.current.error?.message).toBe('boom');
    await tick(1000);
    expect(result.current).toMatchObject({ data: 'b', error: null });
  });

  it('ignores an answer that arrives after a newer one and after unmount', async () => {
    const resolvers: Array<(value: string) => void> = [];
    const fetcher = vi.fn(() => new Promise<string>((resolve) => resolvers.push(resolve)));
    const { result, unmount } = renderHook(() => usePolledResource(fetcher, 1000));
    await tick(1000);
    expect(resolvers).toHaveLength(2);
    await act(async () => { resolvers[1]!('new'); });
    await act(async () => { resolvers[0]!('old'); });
    expect(result.current.data).toBe('new');
    unmount();
    await act(async () => { resolvers.forEach((resolve) => resolve('late')); });
    expect(result.current.data).toBe('new');
  });
});
