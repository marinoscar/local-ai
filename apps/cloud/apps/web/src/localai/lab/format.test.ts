import { backendLabel, formatAgo, formatClock, formatGiB, shortCatalog, vramPercent } from '@localai/ui';

describe('formatGiB', () => {
  it.each<[number, string]>([
    [62464, '61'],
    [98304, '96'],
    [11469, '11.2'],
    [12288, '12'],
    [10240, '10'],
    [10239, '10'],
    [10343, '10.1'],
    [9216, '9'],
    [8192, '8'],
    [1536, '1.5'],
    [0, '0'],
  ])('%i MiB -> %s', (mib, expected) => {
    expect(formatGiB(mib)).toBe(expected);
  });
});

describe('vramPercent', () => {
  it.each<[number, number, number]>([
    [0, 100, 0],
    [61, 96, 64],
    [90, 100, 90],
    [100, 100, 100],
    [150, 100, 100],
    [5, 0, 0],
    [-5, 100, 0],
  ])('%i of %i -> %i%%', (used, total, expected) => {
    expect(vramPercent(used, total)).toBe(expected);
  });
});

describe('formatAgo', () => {
  const now = new Date('2026-11-03T12:00:00.000Z');
  const ago = (seconds: number) => new Date(now.getTime() - seconds * 1000).toISOString();
  it.each<[number, string]>([
    [0, 'just now'],
    [9, 'just now'],
    [10, '10 s ago'],
    [59, '59 s ago'],
    [60, '1 min ago'],
    [3599, '59 min ago'],
    [3600, '1 h ago'],
    [3 * 3600, '3 h ago'],
    [47 * 3600, '47 h ago'],
    [48 * 3600, '2 days ago'],
    [6 * 86400, '6 days ago'],
  ])('%i s -> %s', (seconds, expected) => {
    expect(formatAgo(ago(seconds), now)).toBe(expected);
  });

  it('is null without an instant or with an unreadable one, and a future instant reads "just now"', () => {
    expect(formatAgo(null, now)).toBeNull();
    expect(formatAgo('not a date', now)).toBeNull();
    expect(formatAgo(new Date(now.getTime() + 60_000).toISOString(), now)).toBe('just now');
  });
});

describe('formatClock', () => {
  it('prints HH:MM in the given zone', () => {
    expect(formatClock('2026-11-03T02:14:00.000Z', 'UTC')).toBe('02:14');
    expect(formatClock('2026-11-03T23:05:00.000Z', 'UTC')).toBe('23:05');
    expect(formatClock('2026-11-03T02:14:00.000Z', 'Asia/Tokyo')).toBe('11:14');
    expect(formatClock(null, 'UTC')).toBeNull();
    expect(formatClock('nope', 'UTC')).toBeNull();
  });
});

describe('shortCatalog and backendLabel', () => {
  it('drops the year from a catalog version and names the backends', () => {
    expect(shortCatalog('2026.11.02-1')).toBe('11.02-1');
    expect(shortCatalog('dev')).toBe('dev');
    expect(backendLabel('cuda')).toBe('CUDA');
    expect(backendLabel('vulkan')).toBe('Vulkan');
    expect(backendLabel('cpu')).toBe('CPU');
  });
});
