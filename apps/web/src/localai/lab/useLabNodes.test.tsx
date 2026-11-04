// The Lab hook: loads once, polls every 5 s only while the page is visible, refreshes the moment the tab returns,
// keeps the last answer on a failed refresh, and ignores an answer that arrives after a newer one or after unmount.
import { act, renderHook } from '@testing-library/react';

import { labFixture } from './fixture';
import { LAB_POLL_MS, useLabNodes } from './useLabNodes';

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

describe('useLabNodes', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('polls every 5 s while the page is visible', async () => {
    const fetchNodes = vi.fn().mockResolvedValue(labFixture());
    const { result } = renderHook(() => useLabNodes(fetchNodes));
    expect(result.current.loading).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(1);
    expect(result.current.loading).toBe(false);
    expect(result.current.data?.nodes).toHaveLength(3);
    expect(result.current.updatedAt).toBeInstanceOf(Date);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS - 1);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS * 3);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(5);
  });

  it('is 5000 ms, as the spec says', () => {
    expect(LAB_POLL_MS).toBe(5000);
  });

  it('stops while the tab is hidden and refreshes at once when it is visible again', async () => {
    const fetchNodes = vi.fn().mockResolvedValue(labFixture());
    renderHook(() => useLabNodes(fetchNodes));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(1);

    act(() => setVisibility('hidden'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS * 6);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(1);

    act(() => setVisibility('visible'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS);
    });
    expect(fetchNodes).toHaveBeenCalledTimes(3);
  });

  it('does not poll at all when it mounts in a hidden tab, until the tab shows', async () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
    const fetchNodes = vi.fn().mockResolvedValue(labFixture());
    renderHook(() => useLabNodes(fetchNodes));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS * 4);
    });
    // The first load happens (the page needs data), the timer does not run.
    expect(fetchNodes).toHaveBeenCalledTimes(1);
  });

  it('stops polling and listening on unmount', async () => {
    const fetchNodes = vi.fn().mockResolvedValue(labFixture());
    const { unmount } = renderHook(() => useLabNodes(fetchNodes));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS * 4);
    });
    act(() => setVisibility('visible'));
    expect(fetchNodes).toHaveBeenCalledTimes(1);
  });

  it('keeps the last answer when a refresh fails, then clears the error on the next success', async () => {
    const fetchNodes = vi.fn().mockResolvedValueOnce(labFixture()).mockRejectedValueOnce(new Error('network down')).mockResolvedValue(labFixture());
    const { result } = renderHook(() => useLabNodes(fetchNodes));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.error).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS);
    });
    expect(result.current.error?.message).toBe('network down');
    expect(result.current.data?.nodes).toHaveLength(3);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS);
    });
    expect(result.current.error).toBeNull();
  });

  it('reports a first-load failure with no data, and refresh() retries', async () => {
    const fetchNodes = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue(labFixture());
    const { result } = renderHook(() => useLabNodes(fetchNodes));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current).toMatchObject({ data: null, loading: false });
    expect(result.current.error?.message).toBe('boom');
    act(() => result.current.refresh());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.data?.nodes).toHaveLength(3);
    expect(result.current.error).toBeNull();
  });

  it('ignores a slow answer that arrives after a newer one', async () => {
    let releaseFirst: (value: ReturnType<typeof labFixture>) => void = () => undefined;
    const stale = labFixture();
    stale.summary.nodes = 99;
    const fresh = labFixture();
    fresh.summary.nodes = 3;
    const fetchNodes = vi
      .fn()
      .mockImplementationOnce(() => new Promise((resolve) => (releaseFirst = resolve)))
      .mockResolvedValue(fresh);
    const { result } = renderHook(() => useLabNodes(fetchNodes));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LAB_POLL_MS);
    });
    expect(result.current.data?.summary.nodes).toBe(3);
    await act(async () => {
      releaseFirst(stale);
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.data?.summary.nodes).toBe(3);
  });
});
