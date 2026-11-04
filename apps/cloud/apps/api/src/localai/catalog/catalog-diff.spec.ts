import { diffCatalogs } from './catalog-diff';

describe('catalog diff', () => {
  it('reports added, removed, changed and unchanged files in catalog order', () => {
    const base = [
      { path: 'CATALOG_VERSION', sha256: '1' },
      { path: 'models.yaml', sha256: '2' },
      { path: 'profiles/old.yaml', sha256: '3' },
    ];
    const next = [
      { path: 'CATALOG_VERSION', sha256: '9' },
      { path: 'models.yaml', sha256: '2' },
      { path: 'ark.yaml', sha256: '4' },
    ];
    expect(diffCatalogs(base, next)).toEqual([
      { path: 'CATALOG_VERSION', change: 'changed', baseSha256: '1', sha256: '9' },
      { path: 'models.yaml', change: 'unchanged', baseSha256: '2', sha256: '2' },
      { path: 'ark.yaml', change: 'added', baseSha256: null, sha256: '4' },
      { path: 'profiles/old.yaml', change: 'removed', baseSha256: '3', sha256: null },
    ]);
  });

  it('treats everything as added when nothing is approved', () => {
    expect(diffCatalogs([], [{ path: 'models.yaml', sha256: '2' }]).map((file) => file.change)).toEqual(['added']);
  });
});
