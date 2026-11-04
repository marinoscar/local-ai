#!/usr/bin/env node
// `npm run icons`: render the app icon and the tray icons as PNG, offline.
//   source: packages/ui/brand/icon.svg  (the single source image, desktop.md section 3.1)
//   out:    build/linux/icons/<n>x<n>.png (16..512), build/linux/icon.png (512, the extraResource),
//           build/linux/tray/tray-<colour>.png (22 px) and tray-<colour>@2x.png (44 px)
// The generated PNGs are committed, so a build needs no tool and no download. Run this after changing the source image.
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const source = join(root, '..', '..', 'packages', 'ui', 'brand', 'icon.svg');
const linux = join(root, 'build', 'linux');

export const ICON_SIZES = [16, 24, 32, 48, 64, 128, 256, 512];
export const TRAY_COLOURS = {
  green: '#2e9e4f',
  blue: '#1e6fd9',
  orange: '#f08a00',
  amber: '#f2c200',
  red: '#d63031',
  grey: '#8a8f98',
};
export const TRAY_SIZES = { '': 22, '@2x': 44 };

const trayShape = (fill) =>
  `<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 512 512">` +
  `<circle cx="256" cy="256" r="224" fill="${fill}" stroke="#14213d" stroke-width="48"/></svg>`;

async function png(svg, size, file) {
  await sharp(Buffer.from(svg), { density: 288 })
    .resize(size, size)
    .png({ compressionLevel: 9 })
    .toFile(file);
}

async function main() {
  const svg = await readFile(source, 'utf8');
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
