import { catalogTagOf, compareCatalogVersions, isCatalogVersion, isGreaterThanAll, parseCatalogVersion, versionOfCatalogTag } from './version';

describe('catalog versions', () => {
  it.each(['2026.11.02-1', '2026.01.31-12', '2024.02.29-3', '1999.12.31-999999999'])('accepts %s', (version) => {
    expect(isCatalogVersion(version)).toBe(true);
  });

  it.each([
    '2026.11.02',
    '2026.11.02-0',
    '2026.11.02-01',
    '2026.13.01-1',
    '2026.02.30-1',
    '2025.02.29-1',
    '26.11.02-1',
    '2026.11.02-1\n',
    ' 2026.11.02-1',
    '2026.11.02-1234567890',
    'stable',
    '',
  ])('rejects %j', (version) => {
    expect(isCatalogVersion(version)).toBe(false);
  });

  it('parses the fields', () => {
    expect(parseCatalogVersion('2026.11.02-7')).toEqual({ year: 2026, month: 11, day: 2, n: 7 });
  });

  it('compares field by field as numbers, not as strings', () => {
    expect(compareCatalogVersions('2026.11.02-10', '2026.11.02-9')).toBeGreaterThan(0);
    expect(compareCatalogVersions('2026.11.02-2', '2026.11.02-10')).toBeLessThan(0);
    expect(compareCatalogVersions('2026.11.03-1', '2026.11.02-99')).toBeGreaterThan(0);
    expect(compareCatalogVersions('2027.01.01-1', '2026.12.31-5')).toBeGreaterThan(0);
    expect(compareCatalogVersions('2026.11.02-3', '2026.11.02-3')).toBe(0);
  });

  it('refuses to compare something that is not a version', () => {
    expect(() => compareCatalogVersions('2026.11.02-1', 'latest')).toThrow();
  });

  it('is greater than all only when strictly newer than each', () => {
    expect(isGreaterThanAll('2026.11.03-1', [])).toBe(true);
    expect(isGreaterThanAll('2026.11.03-1', ['2026.11.02-9', '2026.11.03-1'])).toBe(false);
    expect(isGreaterThanAll('2026.11.03-2', ['2026.11.02-9', '2026.11.03-1'])).toBe(true);
    expect(isGreaterThanAll('2026.11.02-10', ['2026.11.02-9'])).toBe(true);
    expect(isGreaterThanAll('2026.11.02-1', ['2026.11.02-1'])).toBe(false);
  });

  it('maps tags to versions and back', () => {
    expect(versionOfCatalogTag('catalog-2026.11.02-1')).toBe('2026.11.02-1');
    expect(versionOfCatalogTag('catalog-stable')).toBeNull();
    expect(versionOfCatalogTag('node-v1.0.0')).toBeNull();
    expect(versionOfCatalogTag('catalog-2026.02.30-1')).toBeNull();
    expect(catalogTagOf('2026.11.02-1')).toBe('catalog-2026.11.02-1');
  });
});
