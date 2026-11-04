#!/usr/bin/env node
// Checks on the packaged app (docs/specs/desktop.md sections 3.3 and 4.11).
//
//   node scripts/packaged-app.mjs verify [dir]
//       dir is the unpacked app (default release/linux-unpacked): the `ai-desktop` binary next to resources/app.asar.
//       Passes only if: the app is packed in an asar (and there is no loose resources/app folder); the Electron fuses
//       read back from the binary are exactly the ones in electron-builder.yml (section 3.3); the asar holds no *.map
//       file and no node_modules/electron.
//   node scripts/packaged-app.mjs test-copy [--from dir] [--to dir]
//       Copies the packaged app to release/test-copy and turns the Node inspector fuse back on in the COPY, so
//       Playwright can drive it. The copy is for tests only; it is never packaged or shipped (release/ is git-ignored).
//
// Exit codes: 0 ok, 1 verification failed, 2 usage error.
import { access, cp, mkdir, rm, stat, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { listPackage } from '@electron/asar';
import { FuseState, FuseV1Options, FuseVersion, flipFuses, getCurrentFuseWire } from '@electron/fuses';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
export const DEFAULT_APP_DIR = join(root, 'release', 'linux-unpacked');
export const DEFAULT_COPY_DIR = join(root, 'release', 'test-copy');
export const EXECUTABLE = 'ai-desktop';
const COPY_MARKER = '.ai-desktop-test-copy';

/** The fuses of electron-builder.yml `electronFuses` (spec section 3.3), by their @electron/fuses option. */
export const EXPECTED_FUSES = Object.freeze({
  [FuseV1Options.RunAsNode]: false,
  [FuseV1Options.EnableCookieEncryption]: true,
  [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
  [FuseV1Options.EnableNodeCliInspectArguments]: false,
  [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
  [FuseV1Options.OnlyLoadAppFromAsar]: true,
  [FuseV1Options.LoadBrowserProcessSpecificV8Snapshot]: false,
  [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
});

const stateName = (state) =>
  state === FuseState.ENABLE ? 'enabled' : state === FuseState.DISABLE ? 'disabled' : state === FuseState.REMOVED ? 'removed' : `unknown (${state})`;

const exists = (path) => access(path).then(() => true, () => false);

/**
 * Verifies an unpacked packaged app.
 * @param {string} appDir the folder holding the `ai-desktop` binary and `resources/`
 * @returns {Promise<{ ok: boolean, problems: string[], checks: string[] }>}
 */
export async function verifyPackagedApp(appDir) {
  const problems = [];
  const checks = [];
  const binary = join(appDir, EXECUTABLE);
  const asar = join(appDir, 'resources', 'app.asar');

  // 1. asar on
  if (await exists(join(appDir, 'resources', 'app'))) problems.push('resources/app exists: the app is not packed in an asar');
  const hasAsar = await exists(asar);
  if (!hasAsar) problems.push('resources/app.asar is missing: asar is off or the build did not run');
  else checks.push('asar: resources/app.asar present');

  // 2. fuses, read back from the binary
  if (!(await exists(binary))) {
    problems.push(`${EXECUTABLE} binary is missing`);
  } else {
    try {
      const wire = await getCurrentFuseWire(binary);
      if (wire.version !== FuseVersion.V1) problems.push(`unsupported fuse wire version ${wire.version}`);
      for (const [index, want] of Object.entries(EXPECTED_FUSES)) {
        const name = FuseV1Options[index];
        const got = wire[index];
        const wantState = want ? FuseState.ENABLE : FuseState.DISABLE;
        if (got !== wantState) problems.push(`fuse ${name} is ${stateName(got)}, expected ${stateName(wantState)}`);
      }
      if (!problems.some((p) => p.startsWith('fuse '))) checks.push(`fuses: all ${Object.keys(EXPECTED_FUSES).length} match electron-builder.yml`);
    } catch (error) {
      problems.push(`cannot read the fuses of ${EXECUTABLE}: ${error instanceof Error ? error.message : String(error)}`);
    }
  }

  // 3. asar content
  if (hasAsar) {
    let entries = [];
    try {
      entries = listPackage(asar, { isPack: false });
    } catch (error) {
      problems.push(`cannot list app.asar: ${error instanceof Error ? error.message : String(error)}`);
    }
    const maps = entries.filter((entry) => entry.endsWith('.map'));
    for (const entry of maps) problems.push(`source map inside the asar: ${entry}`);
    const electron = entries.filter((entry) => /(^|\/)node_modules\/electron(\/|$)/.test(entry));
    if (electron.length > 0) problems.push(`node_modules/electron inside the asar (${electron.length} entries, first ${electron[0]})`);
    if (maps.length === 0 && electron.length === 0 && entries.length > 0) checks.push(`asar content: ${entries.length} entries, no *.map, no node_modules/electron`);
  }
  return { ok: problems.length === 0, problems, checks };
}

/**
 * Copies the packaged app and re-enables the Node inspector fuse in the copy (for Playwright).
 * Refuses to delete an existing target that this command did not create.
 */
export async function makeTestCopy(fromDir, toDir) {
  const from = resolve(fromDir);
  const to = resolve(toDir);
  if (!(await exists(join(from, EXECUTABLE)))) throw new Error(`no ${EXECUTABLE} binary in ${from}: run \`npm run dist\` first`);
  if (to === from || to.startsWith(`${from}/`) || from.startsWith(`${to}/`)) throw new Error('the copy and the packaged app must be separate folders');
  if (await exists(to)) {
    if (!(await exists(join(to, COPY_MARKER)))) throw new Error(`${to} exists and is not a test copy: refusing to delete it`);
    await rm(to, { recursive: true, force: true });
  }
  await mkdir(dirname(to), { recursive: true });
  await cp(from, to, { recursive: true, verbatimSymlinks: true });
  await flipFuses(join(to, EXECUTABLE), { version: FuseVersion.V1, [FuseV1Options.EnableNodeCliInspectArguments]: true });
  await writeFile(join(to, COPY_MARKER), 'A test copy of the packaged app (inspect re-enabled). Never ship this folder.\n');
  return to;
}

function option(args, name) {
  const at = args.indexOf(name);
  return at >= 0 ? args[at + 1] : undefined;
}

async function main(argv) {
  const [command, ...args] = argv;
  if (command === 'verify') {
    const dir = args[0] && !args[0].startsWith('--') ? args[0] : DEFAULT_APP_DIR;
    if (!(await exists(dir)) || !(await stat(dir)).isDirectory()) {
      console.error(`verify: ${dir} is not a folder: run \`npm run dist\` first`);
      return 1;
    }
    const result = await verifyPackagedApp(dir);
    for (const line of result.checks) console.log(`ok   ${line}`);
    for (const line of result.problems) console.error(`FAIL ${line}`);
    console.log(result.ok ? `verify: passed (${dir})` : `verify: FAILED (${result.problems.length} problem(s)) (${dir})`);
    return result.ok ? 0 : 1;
  }
  if (command === 'test-copy') {
    const to = await makeTestCopy(option(args, '--from') ?? DEFAULT_APP_DIR, option(args, '--to') ?? DEFAULT_COPY_DIR);
    console.log(`test-copy: wrote ${to} (inspect re-enabled, for tests only)`);
    return 0;
  }
  console.error('usage: packaged-app.mjs verify [dir] | test-copy [--from dir] [--to dir]');
  return 2;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main(process.argv.slice(2)).catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    return 1;
  });
}
