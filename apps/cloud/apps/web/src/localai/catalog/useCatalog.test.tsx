// The hooks: the list polls every 5 s (2 s while a lint is awaited), one version polls every 2 s while its lint has no
// verdict and then stops, a hidden tab polls nothing, and the diff names what it compared against.
import { act, renderHook } from '@testing-library/react';

import { ApiError } from '@marinoscar/platform-web/core';

import { BAD_MODELS_YAML, fakeCatalog, files, summary, wireframeVersions } from './fixture';
import { isChecking, LINT_POLL_MS, LIST_POLL_MS, useCatalogDiff, useCatalogVersion, useCatalogVersions } from './useCatalog';

const flush = async (ms = 0) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

describe('isChecking', () => {
  it('is true while no verdict exists, or a re-lint is awaited, and never for history', () => {
    expect(isChecking(summary('2026.11.03-1', 'draft', { lintedAt: null }), {})).toBe(true);
    expect(isChecking(summary('2026.11.03-1', 'valid'), {})).toBe(false);
    expect(isChecking(summary('2026.11.03-1', 'valid', { lintedAt: 'T1' }), { '2026.11.03-1': 'T1' })).toBe(true);
    expect(isChecking(summary('2026.11.03-1', 'valid', { lintedAt: 'T2' }), { '2026.11.03-1': 'T1' })).toBe(false);
    expect(isChecking(summary('2026.11.02-1', 'approved', { lintedAt: null }), {})).toBe(false);
    expect(isChecking(summary('2026.10.28-2', 'superseded', { lintedAt: null }), {})).toBe(false);
  });
});

describe('useCatalogVersions', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility('visible');
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('loads, then polls every 5 s', async () => {
    const fake = fakeCatalog();
    const { result } = renderHook(() => useCatalogVersions(fake.api, {}));
    expect(result.current.loading).toBe(true);
    await flush();
    expect(result.current.data?.map((item) => item.version)).toEqual(['2026.11.03-1', '2026.11.02-1', '2026.10.28-2']);
    expect(result.current.loading).toBe(false);
    await flush(LIST_POLL_MS - 1);
    expect(fake.calls.list).toHaveBeenCalledTimes(1);
    await flush(1);
    expect(fake.calls.list).toHaveBeenCalledTimes(2);
  });

  it('polls every 2 s while a version waits for its lint', async () => {
    const fake = fakeCatalog([summary('2026.11.03-1', 'draft', { lintedAt: null }), ...wireframeVersions().slice(1)]);
    renderHook(() => useCatalogVersions(fake.api, {}));
    await flush();
    await flush(LINT_POLL_MS * 3);
    expect(fake.calls.list).toHaveBeenCalledTimes(4);
  });

  it('polls nothing while the tab is hidden and refreshes the moment it returns', async () => {
    const fake = fakeCatalog();
    renderHook(() => useCatalogVersions(fake.api, {}));
    await flush();
    act(() => setVisibility('hidden'));
    await flush(LIST_POLL_MS * 4);
    expect(fake.calls.list).toHaveBeenCalledTimes(1);
    act(() => setVisibility('visible'));
    await flush();
    expect(fake.calls.list).toHaveBeenCalledTimes(2);
  });

  it('keeps the last answer on screen when a refresh fails, and clears the error on the next success', async () => {
    const fake = fakeCatalog();
    const { result } = renderHook(() => useCatalogVersions(fake.api, {}));
    await flush();
    fake.calls.list.mockRejectedValueOnce(new ApiError('boom', 500));
    await flush(LIST_POLL_MS);
    expect(result.current.error).toBeInstanceOf(Error);
    expect(result.current.data).toHaveLength(3);
    await flush(LIST_POLL_MS);
    expect(result.current.error).toBeNull();
  });
});

describe('useCatalogVersion', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility('visible');
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('polls the version every 2 s while lintedAt is null, then stops', async () => {
    const fake = fakeCatalog([summary('2026.11.03-1', 'draft', { lintedAt: null })]);
    const { result } = renderHook(() => useCatalogVersion(fake.api, '2026.11.03-1'));
    await flush();
    expect(result.current.data?.lintedAt).toBeNull();
    await flush(LINT_POLL_MS);
    await flush(LINT_POLL_MS);
    expect(fake.calls.get).toHaveBeenCalledTimes(3);
    fake.rows.get('2026.11.03-1')!.lintedAt = '2026-11-03T12:00:00.000Z';
    fake.rows.get('2026.11.03-1')!.status = 'valid';
    await flush(LINT_POLL_MS);
    expect(result.current.data?.status).toBe('valid');
    await flush(60000);
    expect(fake.calls.get).toHaveBeenCalledTimes(4);
  });

  it('does not poll a version that already has a verdict, and reloads when the token changes', async () => {
    const fake = fakeCatalog();
    const { result, rerender } = renderHook(({ token }) => useCatalogVersion(fake.api, '2026.11.03-1', token), { initialProps: { token: 'a' } });
    await flush();
    await flush(30000);
    expect(fake.calls.get).toHaveBeenCalledTimes(1);
    rerender({ token: 'b' });
    await flush();
    expect(fake.calls.get).toHaveBeenCalledTimes(2);
    expect(result.current.data?.lintResult?.problems).toHaveLength(2);
  });

  it('forgets the old version when another is asked for, and does nothing for null', async () => {
    const fake = fakeCatalog();
    const { result, rerender } = renderHook(({ version }) => useCatalogVersion(fake.api, version), { initialProps: { version: '2026.11.03-1' as string | null } });
    await flush();
    expect(result.current.data?.version).toBe('2026.11.03-1');
    rerender({ version: '2026.11.02-1' });
    await flush();
    expect(result.current.data?.version).toBe('2026.11.02-1');
    rerender({ version: null });
    await flush();
    expect(result.current.loading).toBe(false);
    expect(fake.calls.get).toHaveBeenCalledTimes(2);
  });

  it('reports a version that does not exist', async () => {
    const fake = fakeCatalog();
    const { result } = renderHook(() => useCatalogVersion(fake.api, '2026.01.01-1'));
    await flush();
    expect(result.current.error).toBeInstanceOf(ApiError);
    expect(result.current.data).toBeNull();
  });
});

describe('useCatalogDiff', () => {
  it('diffs against the approved version the API names', async () => {
    const fake = fakeCatalog();
    const { result } = renderHook(() => useCatalogDiff(fake.api, '2026.11.03-1', 'approved'));
    await vi.waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.against).toBe('2026.11.02-1');
    expect(fake.calls.get).toHaveBeenCalledWith('2026.11.03-1', 'approved');
    expect(fake.calls.get).toHaveBeenCalledWith('2026.11.02-1');
    expect(result.current.files.map((file) => [file.path, file.change])).toEqual([
      ['CATALOG_VERSION', 'changed'],
      ['models.yaml', 'changed'],
      ['ark.yaml', 'unchanged'],
      ['profiles/small-8gb.yaml', 'unchanged'],
    ]);
    expect(result.current.summary).toEqual({ filesChanged: 2, added: 2, removed: 2 });
  });

  it('diffs against a named version', async () => {
    const fake = fakeCatalog([summary('2026.11.02-1', 'approved'), summary('2026.10.28-2', 'superseded')], {
      filesOf: (version) => files(version, version === '2026.11.02-1' ? 'new\n' : BAD_MODELS_YAML),
    });
    const { result } = renderHook(() => useCatalogDiff(fake.api, '2026.11.02-1', '2026.10.28-2'));
    await vi.waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.against).toBe('2026.10.28-2');
    expect(result.current.files.find((file) => file.path === 'models.yaml')?.change).toBe('changed');
  });

  it('treats every file as added when nothing is approved', async () => {
    const fake = fakeCatalog([summary('2026.11.03-1', 'valid')]);
    const { result } = renderHook(() => useCatalogDiff(fake.api, '2026.11.03-1', 'approved'));
    await vi.waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.against).toBeNull();
    expect(result.current.files.every((file) => file.change === 'added')).toBe(true);
  });

  it('reports a failure', async () => {
    const fake = fakeCatalog();
    fake.calls.get.mockRejectedValue(new Error('network'));
    const { result } = renderHook(() => useCatalogDiff(fake.api, '2026.11.03-1', 'approved'));
    await vi.waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error?.message).toBe('network');
  });
});
