import { readFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { EXPECTED_FUSES } from '../../scripts/packaged-app.mjs';
import { FuseV1Options } from '@electron/fuses';
import { ICON_SIZES, TRAY_COLOURS, TRAY_SIZES } from '../../scripts/icons.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');

/** width and height from a PNG's IHDR chunk. */
function pngSize(file: string): { width: number; height: number } {
  const buf = readFileSync(file);
  expect(buf.subarray(0, 8).toString('hex')).toBe('89504e470d0a1a0a');
  return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20) };
}

describe('committed icons (npm run icons)', () => {
  it.each(ICON_SIZES)('icons/%ix%i.png has that size', (size) => {
    expect(pngSize(join(root, 'build/linux/icons', `${size}x${size}.png`))).toEqual({ width: size, height: size });
  });

  it('icon.png (the extraResource) is 512 px', () => {
    expect(pngSize(join(root, 'build/linux/icon.png'))).toEqual({ width: 512, height: 512 });
  });

  it('has a 22 px and a 44 px @2x tray icon for each of the six colours', () => {
    expect(Object.keys(TRAY_COLOURS).sort()).toEqual(['amber', 'blue', 'green', 'grey', 'orange', 'red']);
    for (const name of Object.keys(TRAY_COLOURS)) {
      for (const [suffix, size] of Object.entries(TRAY_SIZES)) {
        expect(pngSize(join(root, 'build/linux/tray', `tray-${name}${suffix}.png`))).toEqual({ width: size, height: size });
      }
    }
  });

  it('the single source image exists', () => {
    expect(statSync(join(root, '..', '..', 'packages/ui/brand/icon.svg')).isFile()).toBe(true);
  });
});

describe('project policy', () => {
  const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as {
    name: string;
    productName: string;
    desktopName: string;
    devDependencies: Record<string, string>;
    dependencies?: Record<string, string>;
  };

  it('has the canonical names (overview.md section 3.8)', () => {
    expect(pkg.name).toBe('ai-desktop');
    expect(pkg.productName).toBe('ai-desktop');
    expect(pkg.desktopName).toBe('ai-desktop.desktop');
  });

  it('pins electron to an exact 44.x version (spec section 3.2)', () => {
    expect(pkg.devDependencies['electron']).toMatch(/^44\.\d+\.\d+$/);
  });

  it('pins every dependency exactly, except the local package', () => {
    for (const [name, range] of Object.entries(pkg.devDependencies)) {
      if (name === '@localai/ui') expect(range).toBe('file:../../packages/ui');
      else expect(range, name).toMatch(/^\d+\.\d+\.\d+$/);
    }
  });

  it('ships nothing through `dependencies` (everything is bundled into out/)', () => {
    expect(pkg.dependencies ?? {}).toEqual({});
  });

  it('loads no network asset from the shell pages', () => {
    const pages = join(root, 'src/renderer');
    const walk = (dir: string): string[] =>
      readdirSync(dir).flatMap((entry) => {
        const path = join(dir, entry);
        return statSync(path).isDirectory() ? walk(path) : [path];
      });
    for (const file of walk(pages)) {
      if (!/\.(html|css|tsx?)$/.test(file)) continue;
      const text = readFileSync(file, 'utf8');
      expect(text, file).not.toMatch(/https?:\/\//);
      expect(text, file).not.toMatch(/@import\s+url/);
    }
  });
});

describe('electron-builder.yml', () => {
  const yml = readFileSync(join(root, 'electron-builder.yml'), 'utf8');

  it('configures exactly the fuses that verify expects', () => {
    const block = /^electronFuses:\n((?:  .+\n)+)/m.exec(yml)?.[1] ?? '';
    const configured = Object.fromEntries(
      block.trim().split('\n').map((line) => {
        const [key, value] = line.trim().split(/:\s*/);
        return [key, value === 'true'];
      }),
    );
    const expected = Object.fromEntries(Object.entries(EXPECTED_FUSES).map(([index, value]) => [FuseV1Options[Number(index)]!.replace(/^./, (c) => c.toLowerCase()), value]));
    expect(configured).toEqual(expected);
  });

  it('builds only deb and AppImage for x64 with the canonical names', () => {
    expect(yml).toContain('appId: cr.marin.localai.desktop');
    expect(yml).toContain('executableName: ai-desktop');
    expect(yml).toContain('target: [ { target: deb, arch: [x64] }, { target: AppImage, arch: [x64] } ]');
    expect(yml).toContain('artifactName: ai-desktop_${version}_${arch}.${ext}');
    expect(yml).toContain('artifactName: ai-desktop-${version}-x86_64.AppImage');
  });

  it('ships the AppArmor profile for the installed binary', () => {
    expect(yml).toContain('appArmorProfile: build/linux/ai-desktop.apparmor');
    const profile = readFileSync(join(root, 'build/linux/ai-desktop.apparmor'), 'utf8');
    expect(profile).toContain('profile ai-desktop /opt/ai-desktop/ai-desktop flags=(unconfined) {');
    expect(profile).toMatch(/^\s+userns,$/m);
  });

  it('references only files that exist', () => {
    expect(statSync(join(root, 'build/linux/icon.png')).isFile()).toBe(true);
    expect(statSync(join(root, 'build/linux/tray')).isDirectory()).toBe(true);
  });
});
