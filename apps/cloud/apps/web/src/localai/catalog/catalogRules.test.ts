import {
  CATALOG_PATH_RE,
  compareCatalogPaths,
  compareVersions,
  dateStamp,
  formatBytes,
  isCatalogPath,
  isCatalogVersion,
  isGreaterThanAll,
  profilePath,
  suggestNextVersion,
  versionOfTag,
} from './catalogRules';

describe('the catalog path rule (the server regex, mirrored)', () => {
  const accepted = ['CATALOG_VERSION', 'models.yaml', 'ark.yaml', 'profiles/small-8gb.yaml', 'profiles/a.yaml', 'profiles/cuda-12gb.yaml', 'profiles/0.yaml'];
  const rejected = [
    '',
    'catalog_version',
    'models.yml',
    'Models.yaml',
    'models.yaml ',
    ' models.yaml',
    'models.yaml\n',
    'models/models.yaml',
    'ark.yaml.bak',
    'profiles/',
    'profiles/.yaml',
    'profiles/Small.yaml',
    'profiles/a_b.yaml',
    'profiles/a b.yaml',
    'profiles/a/b.yaml',
    'profiles/../models.yaml',
    '../models.yaml',
    '/models.yaml',
    'profiles/a.yml',
    'profiles/a.yaml/',
    'README.md',
    'extra.yaml',
  ];

  it.each(accepted)('accepts %s', (path) => {
    expect(isCatalogPath(path)).toBe(true);
  });

  it.each(rejected)('rejects %j', (path) => {
    expect(isCatalogPath(path)).toBe(false);
  });

  it('is the same pattern as the API and the agent', () => {
    expect(CATALOG_PATH_RE.source).toBe('^(CATALOG_VERSION|models\\.yaml|ark\\.yaml|profiles\\/[a-z0-9-]+\\.yaml)$');
  });

  it('builds a profile path only from a name the rule accepts', () => {
    expect(profilePath('small-8gb')).toBe('profiles/small-8gb.yaml');
    expect(profilePath('  small-8gb ')).toBe('profiles/small-8gb.yaml');
    expect(profilePath('')).toBeNull();
    expect(profilePath('Small')).toBeNull();
    expect(profilePath('a/b')).toBeNull();
    expect(profilePath('a.yaml')).toBeNull();
  });
});

describe('catalog versions', () => {
  it('accepts YYYY.MM.DD-N with a real date and a counter without a leading zero', () => {
    for (const version of ['2026.11.03-1', '2026.02.28-10', '2028.02.29-3', '2026.12.31-999999999']) expect(isCatalogVersion(version)).toBe(true);
    for (const version of ['2026.11.03', '2026.11.03-0', '2026.11.03-01', '2026.13.01-1', '2026.02.30-1', '2027.02.29-1', '26.11.03-1', '2026-11-03-1', 'catalog-2026.11.03-1', '2026.11.03-1000000000', '']) {
      expect(isCatalogVersion(version)).toBe(false);
    }
  });

  it('orders by date, then by the counter as a number: 2026.11.03-1 > 2026.11.02-12 > 2026.11.02-2', () => {
    expect(compareVersions('2026.11.03-1', '2026.11.02-12')).toBeGreaterThan(0);
    expect(compareVersions('2026.11.02-12', '2026.11.02-2')).toBeGreaterThan(0);
    expect(compareVersions('2026.11.03-1', '2026.11.02-2')).toBeGreaterThan(0);
    expect(compareVersions('2026.11.02-2', '2026.11.02-12')).toBeLessThan(0);
    expect(compareVersions('2026.11.02-2', '2026.11.02-2')).toBe(0);
    expect(compareVersions('2027.01.01-1', '2026.12.31-9')).toBeGreaterThan(0);
    const sorted = ['2026.11.02-2', '2026.11.03-1', '2026.11.02-12', '2026.10.28-30'].sort(compareVersions);
    expect(sorted).toEqual(['2026.10.28-30', '2026.11.02-2', '2026.11.02-12', '2026.11.03-1']);
  });

  it('refuses to compare what is not a version', () => {
    expect(() => compareVersions('nope', '2026.11.03-1')).toThrow();
  });

  it('is greater than all only when strictly greater than every existing version', () => {
    const existing = ['2026.11.02-1', '2026.11.02-12', '2026.10.28-2'];
    expect(isGreaterThanAll('2026.11.03-1', existing)).toBe(true);
    expect(isGreaterThanAll('2026.11.02-13', existing)).toBe(true);
    expect(isGreaterThanAll('2026.11.02-12', existing)).toBe(false);
    expect(isGreaterThanAll('2026.11.02-2', existing)).toBe(false);
    expect(isGreaterThanAll('2026.11.03-1', [])).toBe(true);
  });

  it('reads the version out of a release tag', () => {
    expect(versionOfTag('catalog-2026.11.02-1')).toBe('2026.11.02-1');
    for (const tag of ['catalog-stable', 'catalog-2026.11.02', 'catalog-2026.02.30-1', 'Catalog-2026.11.02-1', 'catalog-2026.11.02-1 ', 'v2026.11.02-1', '2026.11.02-1', 'catalog-']) {
      expect(versionOfTag(tag)).toBeNull();
    }
  });

  it('suggests a version that is greater than every existing one', () => {
    const today = new Date('2026-11-03T22:30:00Z');
    expect(dateStamp(today)).toBe('2026.11.03');
    expect(suggestNextVersion([], today)).toBe('2026.11.03-1');
    expect(suggestNextVersion(['2026.11.02-1'], today)).toBe('2026.11.03-1');
    expect(suggestNextVersion(['2026.11.03-1', '2026.11.02-1'], today)).toBe('2026.11.03-2');
    expect(suggestNextVersion(['2026.11.03-9', '2026.11.03-10'], today)).toBe('2026.11.03-11');
    // A version dated in the future (a clock that is behind): count on from it, never suggest something not greater.
    expect(suggestNextVersion(['2026.12.01-4'], today)).toBe('2026.12.01-5');
    for (const existing of [[], ['2026.11.03-1'], ['2030.01.01-1']]) expect(isGreaterThanAll(suggestNextVersion(existing, today), existing)).toBe(true);
  });
});

describe('small helpers', () => {
  it('orders the file tree: CATALOG_VERSION, models, ark, then profiles by name', () => {
    expect(['profiles/b.yaml', 'ark.yaml', 'profiles/a.yaml', 'CATALOG_VERSION', 'models.yaml'].sort(compareCatalogPaths)).toEqual([
      'CATALOG_VERSION',
      'models.yaml',
      'ark.yaml',
      'profiles/a.yaml',
      'profiles/b.yaml',
    ]);
  });

  it('formats sizes', () => {
    expect(formatBytes(12)).toBe('12 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(2 * 1024 * 1024)).toBe('2.00 MB');
  });
});
