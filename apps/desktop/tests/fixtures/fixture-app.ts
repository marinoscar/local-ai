// Builds a tiny stand-in for an unpacked packaged app (a binary carrying an Electron fuse wire plus resources/app.asar),
// so scripts/packaged-app.mjs can be tested without a real Electron build.
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { createPackage } from '@electron/asar';
import { EXPECTED_FUSES } from '../../scripts/packaged-app.mjs';

/** The fuse sentinel string of @electron/fuses (constants.js). */
const SENTINEL = 'dL7pKGdnNz796PbbjQWNKmHXBZaB9tsX';
const ENABLE = 0x31;
const DISABLE = 0x30;
/** Electron 44 ships a wire of 9 fuses: the 8 of electron-builder.yml plus WasmTrapHandlers. */
const WIRE_LENGTH = 9;

export interface FixtureOptions {
  /** Fuse index -> enabled. Defaults to exactly the expected configuration. */
  fuses?: Record<number, boolean>;
  /** Files inside the asar, by relative path. Defaults to a minimal good app. */
  asarFiles?: Record<string, string>;
  /** Pack nothing and leave a loose resources/app folder instead. */
  noAsar?: boolean;
  /** Leave out the binary. */
  noBinary?: boolean;
}

export const GOOD_ASAR_FILES: Record<string, string> = {
  'package.json': '{"name":"ai-desktop","main":"./out/main/index.js"}',
  'out/main/index.js': '"use strict";',
  'out/renderer/topbar/index.html': '<!doctype html>',
};

export function fuseWire(fuses: Record<number, boolean>): Buffer {
  const states = Array.from({ length: WIRE_LENGTH }, (_, index) => {
    const value = fuses[index];
    return value === undefined ? DISABLE : value ? ENABLE : DISABLE;
  });
  return Buffer.concat([Buffer.from(SENTINEL), Buffer.from([1, WIRE_LENGTH]), Buffer.from(states)]);
}

async function put(path: string, content: string | Buffer): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, content);
}

/** Returns the app folder; the caller removes `dirname(folder)` (see removeFixture). */
export async function makeFixtureApp(options: FixtureOptions = {}): Promise<string> {
  const base = await mkdtemp(join(tmpdir(), 'ai-desktop-fixture-'));
  const app = join(base, 'linux-unpacked');
  await mkdir(join(app, 'resources'), { recursive: true });
  if (!options.noBinary) {
    await put(join(app, 'ai-desktop'), Buffer.concat([Buffer.from('\x7fELF fixture '), fuseWire(options.fuses ?? { ...EXPECTED_FUSES }), Buffer.from(' tail')]));
  }
  const files = options.asarFiles ?? GOOD_ASAR_FILES;
  const source = join(base, 'asar-src');
  for (const [name, content] of Object.entries(files)) await put(join(source, name), content);
  if (options.noAsar) await mkdir(join(app, 'resources', 'app'), { recursive: true });
  else await createPackage(source, join(app, 'resources', 'app.asar'));
  return app;
}

export async function removeFixture(app: string): Promise<void> {
  await rm(dirname(app), { recursive: true, force: true });
}
