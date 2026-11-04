#!/usr/bin/env node
// `npm run dist`: wraps electron-builder with --publish never (docs/specs/desktop.md section 3.3).
//   node scripts/dist.mjs                    -> deb + AppImage for x64 (the targets of electron-builder.yml)
//   node scripts/dist.mjs --linux --x64      -> the same, explicit (what CI runs)
// Other arguments pass through to electron-builder. Nothing is ever uploaded: the `publish` block of
// electron-builder.yml only produces update metadata. The app is built first when out/ is missing.
import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');

function run(script, args) {
  const result = spawnSync(process.execPath, [join(root, 'node_modules', script), ...args], { cwd: root, stdio: 'inherit' });
  if (result.status !== 0) process.exit(result.status ?? 1);
}

// Drop any --publish the caller passed: it is always `never` here.
const passthrough = [];
const argv = process.argv.slice(2);
for (let i = 0; i < argv.length; i++) {
  if (argv[i] === '--publish') i++;
  else if (!argv[i].startsWith('--publish=')) passthrough.push(argv[i]);
}
if (!existsSync(join(root, 'out', 'main', 'index.js'))) run('electron-vite/bin/electron-vite.js', ['build']);
const platformArgs = passthrough.length > 0 ? passthrough : ['--linux', '--x64'];
run('electron-builder/cli.js', [...platformArgs, '--publish', 'never']);
