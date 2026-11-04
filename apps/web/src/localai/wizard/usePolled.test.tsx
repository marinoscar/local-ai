// The wizard's poll: reads at once, every interval while visible, stops while hidden and when disabled, refreshes when the
// tab returns, keeps the last answer on a failed refresh, and `set` wins over a read still in flight.
import { act, renderHook } from '@testing-library/react';

import { usePolled } from './usePolled';

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

beforeEach(() => {
  vi.useFakeTimers();
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
});
afterEach(() => vi.useRealTimers());

const tick = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

describe('usePolled', () => {
  it('reads at once and then every interval', async () => {
    const read = vi.fn().mockResolvedValue('a');
    const { result } = renderHook(() => usePolled(read, 3000));
    await tick(0);
    expect(read).toHaveBeenCalledTimes(1);
    expect(result.current).toMatchObject({ data: 'a', loading: false, error: null });
    await tick(2999);
    expect(read).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(read).toHaveBeenCalledTimes(2);
  });

  it('stops while the tab is hidden and reads again the moment it returns', async () => {
    const read = vi.fn().mockResolvedValue('a');
    renderHook(() => usePolled(read, 3000));
    await tick(0);
    act(() => setVisibility('hidden'));
    await tick(10_000);
    expect(read).toHaveBeenCalledTimes(1);
    act(() => setVisibility('visible'));
    await tick(0);
    expect(read).toHaveBeenCalledTimes(2);
  });

  it('does not read at all while disabled, and starts when enabled', async () => {
    const read = vi.fn().mockResolvedValue('a');
    const { rerender } = renderHook(({ on }) => usePolled(read, 3000, on), { initialProps: { on: false } });
    await tick(10_000);
    expect(read).not.toHaveBeenCalled();
    rerender({ on: true });
    await tick(0);
    expect(read).toHaveBeenCalledTimes(1);
  });

  it('keeps the last answer when a refresh fails, and clears the error on the next success', async () => {
    const read = vi.fn().mockResolvedValueOnce('a').mockRejectedValueOnce(new Error('network')).mockResolvedValue('b');
    const { result } = renderHook(() => usePolled(read, 1000));
    await tick(0);
    await tick(1000);
    expect(result.current.data).toBe('a');
    expect(result.current.error?.message).toBe('network');
    await tick(1000);
    expect(result.current).toMatchObject({ data: 'b', error: null });
  });

  it('set() replaces the data and beats an older read still in flight', async () => {
    let finish!: (value: string) => void;
    const read = vi.fn().mockReturnValue(new Promise<string>((resolve) => (finish = resolve)));
    const { result } = renderHook(() => usePolled(read, 3000));
    act(() => result.current.set('from action'));
    await act(async () => finish('stale read'));
    expect(result.current.data).toBe('from action');
  });

  it('ignores an answer that arrives after unmount', async () => {
    let finish!: (value: string) => void;
    const read = vi.fn().mockReturnValue(new Promise<string>((resolve) => (finish = resolve)));
    const { unmount } = renderHook(() => usePolled(read, 3000));
    unmount();
    await act(async () => finish('late'));
    expect(read).toHaveBeenCalledTimes(1);
  });
});
