#!/usr/bin/env node
// =============================================================================
// scripts/rename.mjs — rebrand an app started from the platform starter
// =============================================================================
//
// Every runtime read of the app's identity goes through
// `packages/shared/identity.json` (the API, the web shell and manifest, the
// CLI, and `platform-infra sync` for infra/). This script writes that file
// and then the few LITERAL targets nothing can derive at runtime:
//
//   package.json `name`        read by npm before any code runs
//   README.md                  prose: title, tagline, CI badge, clone path
//   install.sh                 fetched by `curl | bash` before the repo exists
//   the two SVG fills          static vectors
//   the test database name     .github/workflows/ci.yml (a service container)
//   apps/cli/package.json bin  read by npm (only with --cli-name)
//
// then re-renders infra/ with `platform-infra sync`.
//
// Two properties, kept from the platform's own codemod:
//   1. Idempotent: the OLD values are read from identity.json, so a rename is
//      always "old -> new" and a second run with the same arguments changes
//      nothing.
//   2. Every edit declares its hit count, and a mismatch is a hard failure: a
//      codemod whose anchor silently stops matching is worse than none.
//
// It never commits, and it never touches node_modules: the platform's own
// fixed strings (its HKDF label, its deploy sentinel and state file) live in
// the packages, where no app can rename them.
// =============================================================================

import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const DEFAULT_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');

/** The layout this script applies to; `detectLayout` in the platform repository's rename.mjs recognises it by this file. */
export const STARTER_MARKER = 'packages/shared/identity.json';

const USAGE = `
Rebrand this app.

  node scripts/rename.mjs --name "Kappa Hub" --repo kappa/kappa-hub [options]

Options:
  --name <string>        Product display name.
  --repo <owner/name>    GitHub repository slug.
  --theme <#rrggbb>      Brand primary colour.
  --background <#rrggbb> First-paint background colour.
  --tagline <string>     One-line description (README subtitle).
  --cli-name <name>      Rename the CLI binary (its ~/.<name>/ and <NAME>_ prefix follow).
  --root <dir>           The app to rebrand (default: this script's repository).
  --dry-run              Show every edit and its hit count; change nothing.
  --force                Proceed with a dirty working tree.
  -h, --help             This message.
`.trimStart();

// =============================================================================
// Validation and derivation (the same rules as the platform's rename.mjs)
// =============================================================================

const HEX = /^#[0-9a-f]{6}$/i;
const REPO_SLUG = /^[\w.-]+\/[\w.-]+$/;
const CLI_NAME = /^[a-z][a-z0-9-]*$/;

export function slugify(name) {
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  return slug.length > 0 ? slug : 'app';
}

/** Everything downstream of one identity, so the old and the new are computed the same way. */
export function derive(identity) {
  const slug = slugify(identity.productName);
  return {
    ...identity,
    slug,
    serviceName: `${slug}-api`,
    testDb: `${slug.replace(/-/g, '_')}_test`,
    envPrefix: `${identity.cliName.toUpperCase().replace(/-/g, '_')}_`,
    repoName: identity.repoSlug.split('/')[1],
    rawUrl: `https://raw.githubusercontent.com/${identity.repoSlug}`,
    cloneUrl: `https://github.com/${identity.repoSlug}.git`,
  };
}

export function validate(opts) {
  const problems = [];
  if (opts.theme !== undefined && !HEX.test(opts.theme)) problems.push(`--theme must be a 6-digit hex colour like #7c3aed, got: ${opts.theme}`);
  if (opts.background !== undefined && !HEX.test(opts.background)) problems.push(`--background must be a 6-digit hex colour, got: ${opts.background}`);
  if (opts.repo !== undefined && !REPO_SLUG.test(opts.repo)) problems.push(`--repo must be owner/name, got: ${opts.repo}`);
  if (opts.cliName !== undefined && !CLI_NAME.test(opts.cliName)) problems.push(`--cli-name must be lowercase letters, digits and hyphens, starting with a letter, got: ${opts.cliName}`);
  if (opts.name !== undefined && opts.name.trim() === '') problems.push('--name cannot be empty.');
  return problems;
}

// =============================================================================
// The starter plan: literal find/replace, each with its declared hit count
// =============================================================================

/** Side-effect free, so the platform repository's guards can derive allowlists from it. */
export function buildStarterPlan(old, next) {
  const edits = [];
  const add = (file, find, replace, expectedHits, why) => {
    if (find !== replace) edits.push({ file, find, replace, expectedHits, why });
  };

  add('package.json', `"name": "${old.slug}",`, `"name": "${next.slug}",`, 1, 'the workspace root name (npm reads it before any code runs)');

  add('README.md', `# ${old.productName}\n`, `# ${next.productName}\n`, 1, 'the README title');
  add('README.md', old.tagline, next.tagline, 1, 'the README subtitle');
  add('README.md', `https://github.com/${old.repoSlug}/actions`, `https://github.com/${next.repoSlug}/actions`, 2, 'the CI badge image and its link');
  add('README.md', `cd ${old.repoName}\n`, `cd ${next.repoName}\n`, 1, 'the clone instructions');
  add('README.md', old.cloneUrl, next.cloneUrl, 1, 'the clone URL');

  add('install.sh', old.rawUrl, next.rawUrl, 1, 'the curl | bash URL in the header');
  add('install.sh', old.cloneUrl, next.cloneUrl, 1, 'the default clone URL');

  add('apps/web/public/favicon.svg', `fill="${old.themeColor}"`, `fill="${next.themeColor}"`, 1, 'the favicon plate');
  add('apps/web/public/icons/source.svg', `fill="${old.themeColor}"`, `fill="${next.themeColor}"`, 1, 'the icon master plate');

  add('.github/workflows/ci.yml', `POSTGRES_DB: ${old.testDb}`, `POSTGRES_DB: ${next.testDb}`, 2, 'the CI test database (service and job)');

  if (next.cliName !== old.cliName) {
    add('apps/cli/package.json', `"${old.cliName}": "./dist/cli.js"`, `"${next.cliName}": "./dist/cli.js"`, 1, 'the bin key (a test pins it to identity.json cliName)');
    add('install.sh', `CLI_NAME="${old.cliName}"`, `CLI_NAME="${next.cliName}"`, 1, 'the installed binary name');
  }
  return edits;
}

function countOccurrences(haystack, needle) {
  let count = 0;
  for (let i = haystack.indexOf(needle); i !== -1; i = haystack.indexOf(needle, i + needle.length)) count += 1;
  return count;
}

/** Applies (or, with dryRun, only checks) the plan; returns what was done and every anchor that did not match. */
export function applyPlan(root, edits, { dryRun }) {
  const applied = [];
  const problems = [];
  const contents = new Map();
  for (const edit of edits) {
    const path = join(root, edit.file);
    if (!existsSync(path)) {
      problems.push(`${edit.file}: file not found`);
      continue;
    }
    const before = contents.get(path) ?? readFileSync(path, 'utf8');
    const hits = countOccurrences(before, edit.find);
    if (hits !== edit.expectedHits) {
      if (hits === 0 && countOccurrences(before, edit.replace) >= edit.expectedHits) {
        applied.push({ ...edit, hits: 0, skipped: true });
        continue;
      }
      problems.push(`${edit.file}: expected ${edit.expectedHits} occurrence(s) of ${JSON.stringify(edit.find)}, found ${hits} (${edit.why})`);
      continue;
    }
    contents.set(path, before.split(edit.find).join(edit.replace));
    applied.push({ ...edit, hits, skipped: false });
  }
  if (!dryRun && problems.length === 0) for (const [path, text] of contents) writeFileSync(path, text);
  return { applied, problems };
}

/** identity.json, written structurally (key order kept). */
export function writeIdentity(root, next, { dryRun }) {
  const identity = {
    productName: next.productName,
    tagline: next.tagline,
    repoSlug: next.repoSlug,
    themeColor: next.themeColor,
    backgroundColor: next.backgroundColor,
    cliName: next.cliName,
  };
  if (!dryRun) writeFileSync(join(root, STARTER_MARKER), `${JSON.stringify(identity, null, 2)}\n`);
  return identity;
}

/** Re-renders infra/ for the new identity (`platform-infra sync`), when the package is installed. */
export function syncInfra(root, { dryRun, infraBin } = {}) {
  const bin = infraBin ?? join(root, 'node_modules', '@marinoscar', 'platform-infra', 'bin', 'platform-infra.mjs');
  if (dryRun) return 'skipped (--dry-run)';
  if (!existsSync(bin)) return 'not installed: run `npm install`, then `npm run platform:infra:sync`';
  execFileSync(process.execPath, [bin, 'sync', '--root', root], { stdio: 'inherit' });
  return 'done';
}

export function parseArgs(argv) {
  const opts = { dryRun: false, force: false, root: DEFAULT_ROOT };
  const takesValue = { '--name': 'name', '--repo': 'repo', '--theme': 'theme', '--background': 'background', '--tagline': 'tagline', '--cli-name': 'cliName', '--root': 'root' };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '-h' || arg === '--help') return { help: true };
    if (arg === '--dry-run') opts.dryRun = true;
    else if (arg === '--force') opts.force = true;
    else if (takesValue[arg]) {
      const value = argv[++i];
      if (value === undefined) throw new Error(`${arg} needs a value.`);
      opts[takesValue[arg]] = arg === '--root' ? resolve(value) : value;
    } else throw new Error(`Unknown argument: ${arg}\n\n${USAGE}`);
  }
  return opts;
}

/**
 * The whole rename against `opts.root`. Returns an exit code; prints the
 * plan, the identity and the steps nothing can do for you.
 */
export function rename(opts, { infraBin } = {}) {
  const root = opts.root ?? DEFAULT_ROOT;
  const changing = ['name', 'repo', 'theme', 'background', 'tagline', 'cliName'].some((k) => opts[k] !== undefined);
  if (!changing) {
    console.error(`rename: nothing to change.\n\n${USAGE}`);
    return 1;
  }
  const invalid = validate(opts);
  if (invalid.length > 0) {
    for (const problem of invalid) console.error(`rename: ${problem}`);
    return 1;
  }
  const manifest = join(root, STARTER_MARKER);
  if (!existsSync(manifest)) {
    console.error(`rename: ${manifest} not found: is ${root} an app started from the platform starter?`);
    return 1;
  }
  if (!opts.dryRun && !opts.force) {
    let dirty = '';
    try {
      dirty = execFileSync('git', ['status', '--porcelain'], { cwd: root, encoding: 'utf8' }).trim();
    } catch {
      console.error('rename: not a git repository; re-run with --force to skip the clean-tree check.');
      return 1;
    }
    if (dirty) {
      console.error('rename: the working tree has uncommitted changes; commit or stash first (a clean tree makes `git checkout .` a full undo), or pass --force.');
      return 1;
    }
  }

  const current = JSON.parse(readFileSync(manifest, 'utf8'));
  const old = derive(current);
  const next = derive({
    productName: opts.name ?? current.productName,
    tagline: opts.tagline ?? current.tagline,
    repoSlug: opts.repo ?? current.repoSlug,
    themeColor: (opts.theme ?? current.themeColor).toLowerCase(),
    backgroundColor: (opts.background ?? current.backgroundColor).toLowerCase(),
    cliName: opts.cliName ?? current.cliName,
  });

  const { applied, problems } = applyPlan(root, buildStarterPlan(old, next), opts);
  console.log(`\n${opts.dryRun ? 'Planned' : 'Applied'} edits (${old.productName} -> ${next.productName}):\n`);
  for (const edit of applied) {
    console.log(edit.skipped ? `  = ${edit.file}  (already applied)` : `  ${opts.dryRun ? '~' : '*'} ${edit.file}  ${edit.hits}x  ${JSON.stringify(edit.find.slice(0, 60))}`);
  }
  if (applied.length === 0) console.log('  (none: every target already carries the new values)');
  if (problems.length > 0) {
    console.error(`\nrename: ${problems.length} anchor(s) did not match as declared; nothing was written:\n`);
    for (const problem of problems) console.error(`    ${problem}`);
    return 1;
  }

  const written = writeIdentity(root, next, opts);
  console.log(`\n  ${opts.dryRun ? '~' : '*'} ${STARTER_MARKER}  (structured write)`);
  for (const [key, value] of Object.entries(written)) console.log(`      ${key}: ${JSON.stringify(value)}`);
  console.log(`\n  infra/ (platform-infra sync): ${syncInfra(root, { dryRun: opts.dryRun, infraBin })}`);

  console.log('\nStill to do by hand:');
  if (next.slug !== old.slug) console.log('  - `npm install`: package-lock.json carries the root name too.');
  if (next.repoSlug !== old.repoSlug) console.log(`  - re-point the remote: git remote set-url origin ${next.cloneUrl}`);
  if (next.cliName !== old.cliName) console.log(`  - machines that ran the old CLI keep ~/.${old.cliName}/ and ${old.envPrefix}* variables; move them by hand.`);
  console.log('  - update the OAuth redirect URIs at your sign-in provider.');
  if (opts.dryRun) console.log('\n(--dry-run: nothing was written.)');
  return 0;
}

const isDirectExecution = process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isDirectExecution) {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (error) {
    console.error(`rename: ${error.message}`);
    process.exit(1);
  }
  if (opts.help) console.log(USAGE);
  else process.exitCode = rename(opts);
}
