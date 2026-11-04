import { act, renderHook } from '@testing-library/react';

import { auditFixture } from './fixture';
import { EMPTY_FILTERS } from './types';
import { AUDIT_POLL_MS, useAudit } from './useAudit';

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

describe('useAudit', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility('visible');
  });
  afterEach(() => {
    vi.useRealTimers();
    setVisibility('visible');
  });

  it('is 5000 ms, as the spec says for every page', () => {
    expect(AUDIT_POLL_MS).toBe(5000);
  });

  it('loads once and refreshes page 1 every 5 s while visible', async () => {
    const fetchAudit = vi.fn().mockResolvedValue(auditFixture());
    const { result } = renderHook(() => useAudit(EMPTY_FILTERS, fetchAudit));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchAudit).toHaveBeenCalledTimes(1);
    expect(fetchAudit).toHaveBeenCalledWith('');
    expect(result.current.data?.items).toHaveLength(10);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUDIT_POLL_MS * 2);
    });
    expect(fetchAudit).toHaveBeenCalledTimes(3);
  });

  it('does not poll a later page (new events would shift its rows)', async () => {
    const fetchAudit = vi.fn().mockResolvedValue(auditFixture());
    renderHook(() => useAudit({ ...EMPTY_FILTERS, page: 2 }, fetchAudit));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUDIT_POLL_MS * 4);
    });
    expect(fetchAudit).toHaveBeenCalledTimes(1);
    expect(fetchAudit).toHaveBeenCalledWith('page=2');
  });

  it('stops while the tab is hidden and refreshes at once when it shows', async () => {
    const fetchAudit = vi.fn().mockResolvedValue(auditFixture());
    renderHook(() => useAudit(EMPTY_FILTERS, fetchAudit));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => setVisibility('hidden'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUDIT_POLL_MS * 5);
    });
    expect(fetchAudit).toHaveBeenCalledTimes(1);
    act(() => setVisibility('visible'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchAudit).toHaveBeenCalledTimes(2);
  });

  it('reads again with the new query when a filter changes, and never lets a slow old answer win', async () => {
    let releaseSlow: (value: ReturnType<typeof auditFixture>) => void = () => undefined;
    const fast = { ...auditFixture(), total: 1 };
    const fetchAudit = vi.fn((query: string) => (query === '' ? new Promise<ReturnType<typeof auditFixture>>((resolve) => (releaseSlow = resolve)) : Promise.resolve(fast)));
    const { result, rerender } = renderHook(({ nodeId }) => useAudit({ ...EMPTY_FILTERS, nodeId }, fetchAudit), { initialProps: { nodeId: '' } });
    rerender({ nodeId: 'ai-n2' });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.data?.total).toBe(1);
    await act(async () => {
      releaseSlow({ ...auditFixture(), total: 999 });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(result.current.data?.total).toBe(1);
  });

  it('keeps the last answer and reports the failure', async () => {
    const fetchAudit = vi.fn().mockResolvedValueOnce(auditFixture()).mockRejectedValue(new Error('boom'));
    const { result } = renderHook(() => useAudit(EMPTY_FILTERS, fetchAudit));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUDIT_POLL_MS);
    });
    expect(result.current.error?.message).toBe('boom');
    expect(result.current.data?.items).toHaveLength(10);
  });

  it('does nothing while disabled (a backwards range)', async () => {
    const fetchAudit = vi.fn().mockResolvedValue(auditFixture());
    renderHook(() => useAudit(EMPTY_FILTERS, fetchAudit, false));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUDIT_POLL_MS * 3);
    });
    expect(fetchAudit).not.toHaveBeenCalled();
  });
});
