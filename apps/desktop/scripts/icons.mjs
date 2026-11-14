#!/usr/bin/env node
// `npm run icons`: render the app icon and the tray icons as PNG, offline.
//   source: packages/ui/brand/icon.svg  (the Corona eclipse, ADR-0014; desktop.md section 3.1)
//           packages/ui/brand/tray.svg  (the eclipse ring; STATUS is replaced by each tray colour)
//           packages/ui/brand/tokens.json (the tray colours)
//   out:    build/linux/icons/<n>x<n>.png (16..512), build/linux/icon.png (512, the extraResource),
//           build/linux/tray/tray-<colour>.png (22 px) and tray-<colour>@2x.png (44 px)
// The generated PNGs are committed, so a build needs no tool and no download. Run this after changing the source image.
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const brand = join(root, '..', '..', 'packages', 'ui', 'brand');
const source = join(brand, 'icon.svg');
const linux = join(root, 'build', 'linux');

export const ICON_SIZES = [16, 24, 32, 48, 64, 128, 256, 512];
/** The tray colours of packages/ui/brand/tokens.json (`tray`), by the names tray.ts uses. */
export const TRAY_COLOURS = {
  green: '#4ADE80',
  blue: '#60A5FA',
  orange: '#FF8A3D',
  amber: '#FBBF24',
  red: '#F87171',
  grey: '#9CA3AF',
};
export const TRAY_SIZES = { '': 22, '@2x': 44 };

let trayTemplate = '';
const trayShape = (fill) => trayTemplate.replaceAll('STATUS', fill);

async function png(svg, size, file) {
  await sharp(Buffer.from(svg), { density: 288 })
    .resize(size, size)
    .png({ compressionLevel: 9 })
    .toFile(file);
}

async function main() {
  const svg = await readFile(source, 'utf8');
  trayTemplate = await readFile(join(brand, 'tray.svg'), 'utf8');
  await mkdir(join(linux, 'icons'), { recursive: true });
  await mkdir(join(linux, 'tray'), { recursive: true });
  for (const size of ICON_SIZES) await png(svg, size, join(linux, 'icons', `${size}x${size}.png`));
  await writeFile(join(linux, 'icon.png'), await readFile(join(linux, 'icons', '512x512.png')));
  for (const [name, fill] of Object.entries(TRAY_COLOURS)) {
    for (const [suffix, size] of Object.entries(TRAY_SIZES)) {
      await png(trayShape(fill), size, join(linux, 'tray', `tray-${name}${suffix}.png`));
    }
  }
  console.log(`icons: wrote ${ICON_SIZES.length} app sizes and ${Object.keys(TRAY_COLOURS).length * 2} tray icons under ${linux}`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) await main();
