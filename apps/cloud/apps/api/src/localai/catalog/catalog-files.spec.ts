import { CatalogValidationError, decodeCatalogText, editorPayloadBytes, filesFromArchive, filesFromEditor, versionOfVersionFile } from './catalog-files';
import { bundleSha256, sha256Hex } from './bundle-hash';

const bytes = (text: string) => Buffer.from(text, 'utf8');

function kind(fn: () => unknown): string {
  try {
    fn();
  } catch (error) {
    if (error instanceof CatalogValidationError) return error.kind;
    throw error;
  }
  return 'accepted';
}

describe('files from an archive', () => {
  const extracted = () =>
    new Map<string, Uint8Array>([
      ['CATALOG_VERSION', bytes('2026.11.02-1\n')],
      ['models.yaml', bytes('schema_version: 1\n')],
      ['profiles/small-8gb.yaml', bytes('id: small-8gb\n')],
    ]);

  it('stores the bytes as they are with their digests, in canonical order', () => {
    const { version, files } = filesFromArchive(extracted(), '2026.11.02-1');
    expect(version).toBe('2026.11.02-1');
    expect(files.map((file) => file.path)).toEqual(['CATALOG_VERSION', 'models.yaml', 'profiles/small-8gb.yaml']);
    expect(files[1]).toEqual({ path: 'models.yaml', sha256: sha256Hex(bytes('schema_version: 1\n')), content: 'schema_version: 1\n' });
    expect(bundleSha256(files)).toHaveLength(64);
  });

  it('accepts a CATALOG_VERSION without a trailing newline', () => {
    const map = extracted();
    map.set('CATALOG_VERSION', bytes('2026.11.02-1'));
    expect(filesFromArchive(map, '2026.11.02-1').version).toBe('2026.11.02-1');
  });

  it('refuses a version file that disagrees with the tag, is missing or is malformed', () => {
    expect(kind(() => filesFromArchive(extracted(), '2026.11.02-2'))).toBe('version_mismatch');
    const missing = extracted();
    missing.delete('CATALOG_VERSION');
    expect(kind(() => filesFromArchive(missing, '2026.11.02-1'))).toBe('missing_version_file');
    const bad = extracted();
    bad.set('CATALOG_VERSION', bytes('2026.11.02-1\nsecond line\n'));
    expect(kind(() => filesFromArchive(bad, '2026.11.02-1'))).toBe('bad_version');
  });

  it('refuses a path outside the layout, invalid UTF-8 and a NUL byte', () => {
    const withExtra = extracted();
    withExtra.set('notes.txt', bytes('x'));
    expect(kind(() => filesFromArchive(withExtra, '2026.11.02-1'))).toBe('bad_path');
    const invalid = extracted();
    invalid.set('models.yaml', Buffer.from([0xff, 0xfe, 0x41]));
    expect(kind(() => filesFromArchive(invalid, '2026.11.02-1'))).toBe('bad_encoding');
    const nul = extracted();
    nul.set('models.yaml', bytes('a\u0000b'));
    expect(kind(() => filesFromArchive(nul, '2026.11.02-1'))).toBe('bad_encoding');
  });

  it('keeps a BOM so the digest equals the digest of the bytes', () => {
    const map = extracted();
    const withBom = Buffer.from('\uFEFFschema_version: 1\n', 'utf8');
    map.set('models.yaml', withBom);
    const { files } = filesFromArchive(map, '2026.11.02-1');
    expect(files.find((file) => file.path === 'models.yaml')?.sha256).toBe(sha256Hex(withBom));
  });

  it('refuses more than 2 MB in total', () => {
    const map = extracted();
    map.set('models.yaml', Buffer.alloc(2 * 1024 * 1024 + 1, 0x61));
    expect(kind(() => filesFromArchive(map, '2026.11.02-1'))).toBe('too_large');
  });
});

describe('files from the editor', () => {
  const input = [
    { path: 'models.yaml', content: 'schema_version: 1\n' },
    { path: 'profiles/small-8gb.yaml', content: 'id: small-8gb\n' },
  ];

  it('writes CATALOG_VERSION from the new version, adding it when absent', () => {
    const files = filesFromEditor(input, '2026.11.03-1');
    expect(files.find((file) => file.path === 'CATALOG_VERSION')?.content).toBe('2026.11.03-1\n');
    expect(files).toHaveLength(3);
  });

  it('rewrites a CATALOG_VERSION the caller sent', () => {
    const files = filesFromEditor([{ path: 'CATALOG_VERSION', content: '1999.01.01-1\n' }, ...input], '2026.11.03-1');
    expect(files.filter((file) => file.path === 'CATALOG_VERSION')).toEqual([
      { path: 'CATALOG_VERSION', sha256: sha256Hex('2026.11.03-1\n'), content: '2026.11.03-1\n' },
    ]);
  });

  it.each(['README.md', '../models.yaml', '/models.yaml', 'profiles/Upper.yaml', 'profiles/a/b.yaml', 'profiles/.yaml', 'models.yaml\n', 'profiles/x.yml'])(
    'refuses the path %j',
    (path) => {
      expect(kind(() => filesFromEditor([{ path, content: 'x' }], '2026.11.03-1'))).toBe('bad_path');
    },
  );

  it('refuses a duplicate path and a NUL character', () => {
    expect(kind(() => filesFromEditor([...input, input[0]], '2026.11.03-1'))).toBe('duplicate_path');
    expect(kind(() => filesFromEditor([{ path: 'models.yaml', content: 'a\u0000' }], '2026.11.03-1'))).toBe('bad_encoding');
  });

  it('allows exactly 2 MB and refuses one byte more (multi-byte characters count as bytes)', () => {
    const version = '2026.11.03-1';
    const room = 2 * 1024 * 1024 - Buffer.byteLength(`${version}\n`);
    expect(kind(() => filesFromEditor([{ path: 'models.yaml', content: 'a'.repeat(room) }], version))).toBe('accepted');
    expect(kind(() => filesFromEditor([{ path: 'models.yaml', content: 'a'.repeat(room + 1) }], version))).toBe('too_large');
    expect(kind(() => filesFromEditor([{ path: 'models.yaml', content: 'é'.repeat(Math.ceil(room / 2) + 1) }], version))).toBe('too_large');
    expect(editorPayloadBytes([{ content: 'é' }])).toBe(2);
  });
});

describe('text decoding and version files', () => {
  it('decodes valid UTF-8 and keeps a BOM character', () => {
    expect(decodeCatalogText(bytes('héllo'), 'models.yaml')).toBe('héllo');
    expect(decodeCatalogText(Buffer.from('\uFEFFa', 'utf8'), 'models.yaml')).toBe('\uFEFFa');
  });

  it('reads one version line only', () => {
    expect(versionOfVersionFile('2026.11.02-1\n')).toBe('2026.11.02-1');
    expect(versionOfVersionFile('2026.11.02-1\r\n')).toBe('2026.11.02-1');
    expect(() => versionOfVersionFile('2026.11.02-1\n\n')).toThrow(CatalogValidationError);
    expect(() => versionOfVersionFile('')).toThrow(CatalogValidationError);
  });
});
