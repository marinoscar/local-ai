#!/usr/bin/env node
// carry-deploy-version.mjs: keep the deployed release version when cloud-deploy-branch.sh republishes deploy/cloud.
//
// Usage: node scripts/carry-deploy-version.mjs <worktree>
//   <worktree> is the deploy-branch worktree AFTER the new tree was copied over it: HEAD is the branch tip, the files
//   on disk are the tree about to be committed.
//
// Why: every `laictl deploy update` bumps the app version and pushes a `chore(release): x.y.z` commit to
// deploy/cloud (apps/cloud/apps/cli/src/app.ts, deploy.versionedManifests). The next publish replaces the whole tree
// with main's apps/cloud, whose manifests still say the older version, so without this the version would fall back
// and the next deploy would re-issue a number that was already deployed (#574).
//
// What it does: reads the manifest list from <worktree>/packages/shared/versioned-manifests.json, compares the
// version on the branch tip with the version in the new tree, and when the tip is higher writes the tip's version
// into every listed manifest and its package-lock.json workspace entry. A higher version on main (a deliberate bump)
// wins. Edits are textual: only the "version" values change, nothing else in the files moves.
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const SEMVER = /^(\d+)\.(\d+)\.(\d+)$/;

function die(message) {
  process.stderr.write(`carry-deploy-version: ${message}\n`);
  process.exit(1);
}

function parse(version) {
  const m = SEMVER.exec(version ?? '');
  return m ? m.slice(1).map(Number) : undefined;
}

function compare(a, b) {
  for (let i = 0; i < 3; i += 1) if (a[i] !== b[i]) return a[i] - b[i];
  return 0;
}

function tipFile(wt, path) {
  try {
    return execFileSync('git', ['-C', wt, 'show', `HEAD:${path}`], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
  } catch {
    return undefined;
  }
}

function versionOf(text) {
  try {
    return JSON.parse(text).version;
  } catch {
    return undefined;
  }
}

const wt = process.argv[2];
if (!wt || !existsSync(join(wt, '.git'))) die('usage: carry-deploy-version.mjs <deploy-branch worktree>');

const listPath = join(wt, 'packages/shared/versioned-manifests.json');
if (!existsSync(listPath)) {
  console.log('= no packages/shared/versioned-manifests.json in the tree; no version to carry.');
  process.exit(0);
}
const manifests = JSON.parse(readFileSync(listPath, 'utf8')).manifests;
if (!Array.isArray(manifests) || manifests.length === 0) die(`${listPath} lists no manifests`);

// The deployed version: the highest one any listed manifest carries on the branch tip.
let deployed;
for (const m of manifests) {
  const v = parse(versionOf(tipFile(wt, m)));
  if (v && (!deployed || compare(v, deployed) > 0)) deployed = v;
}
if (!deployed) {
  console.log('= the branch tip carries no release version; nothing to carry.');
  process.exit(0);
}

let incoming;
for (const m of manifests) {
  const path = join(wt, m);
  if (!existsSync(path)) die(`${m} is listed in versioned-manifests.json but is not in the tree`);
  const v = parse(versionOf(readFileSync(path, 'utf8')));
  if (!v) die(`${m} has no MAJOR.MINOR.PATCH version`);
  if (!incoming || compare(v, incoming) > 0) incoming = v;
}

const deployedText = deployed.join('.');
if (compare(incoming, deployed) >= 0) {
  console.log(`= the new tree's version ${incoming.join('.')} is not below the deployed ${deployedText}; kept.`);
  process.exit(0);
}

// Write the deployed version into each manifest's top-level "version" and its lockfile workspace entry.
const TOP_VERSION = /^( {2}"version": ")[^"]*(")/m;
for (const m of manifests) {
  const path = join(wt, m);
  const text = readFileSync(path, 'utf8');
  if (!TOP_VERSION.test(text)) die(`${m}: no top-level "version" line to rewrite`);
  writeFileSync(path, text.replace(TOP_VERSION, `$1${deployedText}$2`));
}
const lockPath = join(wt, 'package-lock.json');
if (existsSync(lockPath)) {
  let lock = readFileSync(lockPath, 'utf8');
  for (const m of manifests) {
    const key = m.replace(/\/package\.json$/, '').replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const entry = new RegExp(`(\\n {4}"${key}": \\{\\n {6}"version": ")[^"]*(")`);
    if (entry.test(lock)) lock = lock.replace(entry, `$1${deployedText}$2`);
  }
  writeFileSync(lockPath, lock);
}
console.log(`✓ carried the deployed version ${deployedText} forward (the new tree had ${incoming.join('.')}).`);
