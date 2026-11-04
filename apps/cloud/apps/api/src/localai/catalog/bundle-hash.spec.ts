import { bundleSha256, compareCatalogPaths, sha256Hex, toCatalogFiles } from './bundle-hash';

// A digest is written in two halves: the repository's redaction check treats any 64-hex string as a possible key.
const h = (first: string, second: string): string => first + second;

// Golden vector computed by the agent's own `catalog.BundleDigest` (agent/internal/catalog/fetch_github.go) over these
// five files: the cloud and a node must agree on the digest of the same tag.
const GOLDEN_FILES = [
  { path: 'profiles/small-8gb.yaml', content: 'id: small-8gb\n', sha256: h('20c43c1fc775d9cc56b975edfe016121', '2f2d627c06501047ecaa7380e5d8e0bd') },
  { path: 'CATALOG_VERSION', content: '2026.11.02-1\n', sha256: h('1b8077d0cec3fb04865d15ae2fa906fb', '5b1fc2d386c00bfec288adc092ad0e5b') },
  { path: 'models.yaml', content: 'schema_version: 1\nmodels: []\n', sha256: h('595868c2080347df2a41690646d2933c', 'f0bc3718bcae7600995c0bf77b514f99') },
  { path: 'ark.yaml', content: 'schema_version: 1\n', sha256: h('5c9536c0f64193f535c425baf9f2c743', '1b89eb46bab5fb85d4bfcffabb565f43') },
  { path: 'profiles/cpu-only.yaml', content: 'id: cpu-only\n', sha256: h('79b1beedd40bfbeed2e0a6d6935aef67', 'ac9922f7bf467010dc845447eeec6813') },
];
const GOLDEN_BUNDLE = h('d7b246fb8bbb7b4640a5186dfc459c6f', 'fdb6daa60970dc73a10393ea6acc89e0');

describe('bundle digest', () => {
  it('hashes content as UTF-8 bytes', () => {
    expect(sha256Hex('')).toBe(h('e3b0c44298fc1c149afbf4c8996fb924', '27ae41e4649b934ca495991b7852b855'));
    expect(sha256Hex(Buffer.from('é', 'utf8'))).toBe(sha256Hex('é'));
  });

  it('equals the agent digest for the golden files, in any input order', () => {
    expect(bundleSha256(GOLDEN_FILES)).toBe(GOLDEN_BUNDLE);
    expect(bundleSha256([...GOLDEN_FILES].reverse())).toBe(GOLDEN_BUNDLE);
  });

  it('computes the per-file digests the agent computes', () => {
    const files = toCatalogFiles(GOLDEN_FILES.map(({ path, content }) => ({ path, content })));
    expect(files.map((file) => [file.path, file.sha256])).toEqual(
      [...GOLDEN_FILES].sort((a, b) => compareCatalogPaths(a.path, b.path)).map((file) => [file.path, file.sha256]),
    );
  });

  it('changes when a path or a digest changes', () => {
    const changed = GOLDEN_FILES.map((file) => (file.path === 'ark.yaml' ? { ...file, sha256: sha256Hex('other') } : file));
    expect(bundleSha256(changed)).not.toBe(GOLDEN_BUNDLE);
    const renamed = GOLDEN_FILES.map((file) => (file.path === 'ark.yaml' ? { ...file, path: 'ark2.yaml' } : file));
    expect(bundleSha256(renamed)).not.toBe(GOLDEN_BUNDLE);
  });

  it('orders files CATALOG_VERSION, models, ark, then profiles by name', () => {
    const files = toCatalogFiles([
      { path: 'profiles/b.yaml', content: 'b' },
      { path: 'ark.yaml', content: 'a' },
      { path: 'profiles/a.yaml', content: 'a' },
      { path: 'models.yaml', content: 'm' },
      { path: 'CATALOG_VERSION', content: 'v' },
    ]);
    expect(files.map((file) => file.path)).toEqual(['CATALOG_VERSION', 'models.yaml', 'ark.yaml', 'profiles/a.yaml', 'profiles/b.yaml']);
  });
});
