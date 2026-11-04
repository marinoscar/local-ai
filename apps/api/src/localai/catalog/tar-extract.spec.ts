import { gzipSync } from 'node:zlib';

import { buildCatalogArchive, buildTar, buildTarGz, repoCatalogFiles, type TarEntry } from '../../../test/localai/helpers/tar-builder';
import { CatalogArchiveError, extractCatalogFiles } from './tar-extract';

const TOP = 'local-ai-catalog-fixture';

async function refusal(archive: Buffer, options?: Parameters<typeof extractCatalogFiles>[1]): Promise<string> {
  try {
    await extractCatalogFiles(archive, options);
  } catch (error) {
    if (error instanceof CatalogArchiveError) return error.code;
    throw error;
  }
  return 'accepted';
}

const catalogFile = (path: string, content = 'x\n'): TarEntry => ({ path: `${TOP}/catalog/${path}`, content });

describe('extractCatalogFiles', () => {
  it('extracts exactly the catalog files of a GitHub-shaped archive and nothing else', async () => {
    const files = await extractCatalogFiles(buildCatalogArchive());
    const expected = repoCatalogFiles();
    expect([...files.keys()].sort()).toEqual([...expected.keys()].sort());
    for (const [path, bytes] of expected) expect(files.get(path)?.equals(bytes)).toBe(true);
  });

  it('ignores a symlink outside catalog/ (the repository has some)', async () => {
    const archive = buildTarGz([
      { path: `${TOP}/`, type: 'dir' },
      { path: `${TOP}/apps/cloud/packages/ui`, type: 'symlink', linkTarget: '../../../packages/ui' },
      { path: `${TOP}/apps/hard`, type: 'hardlink', linkTarget: `${TOP}/README.md` },
      catalogFile('models.yaml'),
    ]);
    expect([...(await extractCatalogFiles(archive)).keys()]).toEqual(['models.yaml']);
  });

  it('reads a path given in a pax extended header', async () => {
    const archive = buildTarGz([{ path: `${TOP}/catalog/profiles/${'a'.repeat(60)}.yaml`, content: 'x\n', pax: true }, catalogFile('models.yaml')]);
    const files = await extractCatalogFiles(archive);
    expect([...files.keys()].sort()).toEqual([`profiles/${'a'.repeat(60)}.yaml`, 'models.yaml'].sort());
  });

  it('keeps empty files and exact bytes (no BOM stripping, no newline changes)', async () => {
    const bytes = Buffer.from('\uFEFFa: 1\r\nb: 2', 'utf8');
    const files = await extractCatalogFiles(buildTarGz([catalogFile('models.yaml', ''), { path: `${TOP}/catalog/ark.yaml`, content: bytes }]));
    expect(files.get('models.yaml')?.length).toBe(0);
    expect(files.get('ark.yaml')?.equals(bytes)).toBe(true);
  });

  describe('refuses', () => {
    it.each([
      ['an absolute path', { path: '/etc/passwd', content: 'x' }],
      ['an absolute path inside the top directory name', { path: `/${TOP}/catalog/models.yaml`, content: 'x' }],
      ['a parent segment', { path: `${TOP}/catalog/../../../etc/cron`, content: 'x' }],
      ['a parent segment outside catalog/', { path: `${TOP}/docs/../../outside`, content: 'x' }],
      ['a backslash', { path: `${TOP}/catalog/profiles\\..\\x.yaml`, content: 'x' }],
      ['a drive path', { path: 'C:/Windows/x', content: 'x' }],
    ] as Array<[string, TarEntry]>)('%s', async (_name, entry) => {
      expect(await refusal(buildTarGz([catalogFile('models.yaml'), entry]))).toBe('unsafe_path');
    });

    it('a parent segment given through a pax header', async () => {
      expect(await refusal(buildTarGz([catalogFile('models.yaml'), { path: `${TOP}/catalog/../../x`, content: 'x', pax: true }]))).toBe('unsafe_path');
    });

    it.each([
      ['a symlink to a file', { path: `${TOP}/catalog/models.yaml`, type: 'symlink', linkTarget: '/etc/passwd' }],
      ['a symlink to outside', { path: `${TOP}/catalog/profiles/evil.yaml`, type: 'symlink', linkTarget: '../../../../etc/passwd' }],
      ['a hardlink', { path: `${TOP}/catalog/ark.yaml`, type: 'hardlink', linkTarget: `${TOP}/README.md` }],
      ['catalog/ itself as a symlink', { path: `${TOP}/catalog`, type: 'symlink', linkTarget: 'docs' }],
    ] as Array<[string, TarEntry]>)('%s', async (_name, entry) => {
      expect(await refusal(buildTarGz([entry]))).toBe('link_entry');
    });

    it.each(['README.md', 'models.yml', 'profiles/Upper.yaml', 'profiles/a/b.yaml', 'profiles/x.yaml.bak', 'extra/models.yaml', 'models.yaml/'.slice(0, -1) + 'x'])(
      'a file outside the catalog layout: %s',
      async (path) => {
        expect(await refusal(buildTarGz([catalogFile('models.yaml'), catalogFile(path)]))).toBe('unexpected_file');
      },
    );

    it('a file listed twice', async () => {
      expect(await refusal(buildTarGz([catalogFile('models.yaml'), catalogFile('models.yaml')]))).toBe('duplicate_file');
    });

    it('more than the catalog size limit', async () => {
      const archive = buildTarGz([catalogFile('models.yaml', 'a'.repeat(1500)), catalogFile('ark.yaml', 'b'.repeat(1500))]);
      expect(await refusal(archive, { maxCatalogBytes: 2000 })).toBe('too_large');
      expect(await refusal(archive, { maxCatalogBytes: 3000 })).toBe('accepted');
    });

    it('a decompression bomb (inflated size over the limit)', async () => {
      const archive = buildTarGz([{ path: `${TOP}/docs/big.bin`, content: Buffer.alloc(5 * 1024 * 1024) }, catalogFile('models.yaml')]);
      expect(await refusal(archive, { maxUncompressedBytes: 1024 * 1024 })).toBe('too_large');
    });

    it('too many entries', async () => {
      const entries: TarEntry[] = Array.from({ length: 30 }, (_, i) => ({ path: `${TOP}/docs/f${i}.md`, content: 'x' }));
      expect(await refusal(buildTarGz([...entries, catalogFile('models.yaml')]), { maxEntries: 10 })).toBe('too_large');
    });

    it('two top-level directories', async () => {
      expect(await refusal(buildTarGz([catalogFile('models.yaml'), { path: 'other-top/catalog/ark.yaml', content: 'x' }]))).toBe('bad_archive');
    });

    it('an archive without a catalog', async () => {
      expect(await refusal(buildTarGz([{ path: `${TOP}/README.md`, content: 'x' }]))).toBe('no_catalog');
      expect(await refusal(buildTarGz([{ path: `${TOP}/catalog/`, type: 'dir' }]))).toBe('no_catalog');
    });

    it('bytes that are not gzip, a truncated archive and a damaged header', async () => {
      expect(await refusal(Buffer.from('not an archive at all'))).toBe('bad_archive');
      const good = buildTarGz([catalogFile('models.yaml', 'a'.repeat(5000))]);
      expect(await refusal(good.subarray(0, good.length - 40))).toBe('bad_archive');
      const tar = buildTar([catalogFile('models.yaml')]);
      tar[10] ^= 0xff;
      expect(await refusal(gzipSync(tar))).toBe('bad_archive');
    });

    it('an empty input', async () => {
      expect(await refusal(Buffer.alloc(0))).toBe('bad_archive');
    });
  });
});
