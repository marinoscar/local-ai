// The lint runner against fake `aictl` scripts (one per exit-code case) and, in the db tier, the real binary.
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import type { CatalogFile } from './catalog.constants';
import { AictlLinter, aictlPath, interpretLint } from './catalog-linter';
import { toCatalogFiles } from './bundle-hash';

const FILES: CatalogFile[] = toCatalogFiles([
  { path: 'CATALOG_VERSION', content: '2026.11.03-1\n' },
  { path: 'models.yaml', content: 'schema_version: 1\n' },
  { path: 'profiles/small-8gb.yaml', content: 'id: small-8gb\n' },
]);

describe('catalog linter', () => {
  let dir: string;
  const saved = process.env.LOCALAI_AICTL_PATH;
  const probe = (name: string) => join(dir, name);

  /** Writes an executable fake `aictl`; `$SPY` is the file the script reports into. */
  function fake(name: string, body: string): string {
    const path = probe(name);
    writeFileSync(path, `#!/bin/sh\n${body}\n`);
    chmodSync(path, 0o755);
    return path;
  }

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'catalog-linter-spec-'));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
    if (saved === undefined) delete process.env.LOCALAI_AICTL_PATH;
    else process.env.LOCALAI_AICTL_PATH = saved;
  });

  it('exit 0 with valid:true is valid, and aictl is called with an argv array and a private temp directory holding the files', async () => {
    const spy = probe('spy.txt');
    process.env.LOCALAI_AICTL_PATH = fake(
      'ok',
      `printf '%s\\n' "$@" > '${spy}'; ls -R "$4" >> '${spy}'; stat -c '%a' "$4" >> '${spy}'; env >> '${spy}'; echo '{"valid":true,"problems":[]}'`,
    );
    process.env.LAI_TEST_SECRET = 'must-not-reach-the-child';
    try {
      const outcome = await new AictlLinter().lint(FILES);
      expect(outcome).toEqual({ status: 'valid', result: { valid: true, problems: [], exitCode: 0 } });
    } finally {
      delete process.env.LAI_TEST_SECRET;
    }
    const lines = readFileSync(spy, 'utf8').split('\n');
    expect(lines.slice(0, 3)).toEqual(['catalog', 'lint', '--json']);
    const lintDir = lines[3];
    expect(lintDir).toContain('-job-');
    expect(readFileSync(spy, 'utf8')).toMatch(/CATALOG_VERSION[\s\S]*models\.yaml[\s\S]*profiles/);
    expect(readFileSync(spy, 'utf8')).toMatch(/\n700\n/);
    expect(readFileSync(spy, 'utf8')).not.toContain('must-not-reach-the-child');
    // The temp directory is gone once the lint is over.
    expect(existsSync(lintDir)).toBe(false);
  });

  it('exit 2 with valid:false is invalid and stores the problems', async () => {
    const problem = { code: 'LAI-CAT-011', file: 'models.yaml', path: 'models[3].source.revision', message: 'revision must be a 40-hex commit' };
    process.env.LOCALAI_AICTL_PATH = fake('invalid', `echo '${JSON.stringify({ valid: false, problems: [problem] })}'; exit 2`);
    expect(await new AictlLinter().lint(FILES)).toEqual({ status: 'invalid', result: { valid: false, problems: [problem], exitCode: 2 } });
  });

  it('any other exit code leaves the version a draft with LAI-CLD-033', async () => {
    process.env.LOCALAI_AICTL_PATH = fake('crash', 'echo boom >&2; exit 1');
    expect(await new AictlLinter().lint(FILES)).toEqual({
      status: 'draft',
      result: { error: { code: 'LAI-CLD-033', reason: 'aictl_failed', message: expect.any(String) }, exitCode: 1 },
    });
  });

  it('a missing binary, an unset path and a non-executable file are aictl_missing', async () => {
    process.env.LOCALAI_AICTL_PATH = probe('does-not-exist');
    expect((await new AictlLinter().lint(FILES)).result.error?.reason).toBe('aictl_missing');
    delete process.env.LOCALAI_AICTL_PATH;
    expect((await new AictlLinter().lint(FILES)).result.error?.reason).toBe('aictl_missing');
    const plain = probe('plain');
    writeFileSync(plain, 'not executable');
    process.env.LOCALAI_AICTL_PATH = plain;
    expect((await new AictlLinter().lint(FILES)).result.error?.reason).toBe('aictl_missing');
  });

  it('kills a run that outlives the timeout', async () => {
    process.env.LOCALAI_AICTL_PATH = fake('slow', 'sleep 20');
    const started = Date.now();
    const outcome = await new AictlLinter(300).lint(FILES);
    expect(outcome.status).toBe('draft');
    expect(outcome.result.error?.reason).toBe('timeout');
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it.each([
    ['garbage on exit 0', 'echo not json'],
    ['valid:false on exit 0', `echo '{"valid":false,"problems":[]}'`],
    ['valid:true on exit 2', `echo '{"valid":true,"problems":[]}'; exit 2`],
    ['a report of the wrong shape', `echo '{"valid":true}'`],
    ['a problem without a code', `echo '{"valid":false,"problems":[{"message":"x"}]}'; exit 2`],
  ])('%s is bad_output', async (_name, script) => {
    process.env.LOCALAI_AICTL_PATH = fake('weird', script);
    const outcome = await new AictlLinter().lint(FILES);
    expect(outcome.status).toBe('draft');
    expect(outcome.result.error).toMatchObject({ code: 'LAI-CLD-033', reason: 'bad_output' });
  });

  it('a report larger than the output ceiling is bad_output', async () => {
    process.env.LOCALAI_AICTL_PATH = fake('flood', 'head -c 3000000 /dev/zero | tr "\\0" "a"');
    expect((await new AictlLinter().lint(FILES)).result.error?.reason).toBe('bad_output');
  });

  it('stores at most 500 problems', () => {
    const problems = Array.from({ length: 800 }, (_, i) => ({ code: 'LAI-CAT-010', file: 'models.yaml', path: `models[${i}]`, message: 'x' }));
    const outcome = interpretLint({ code: 2, killed: false, stdout: JSON.stringify({ valid: false, problems }) });
    expect(outcome.result.problems).toHaveLength(500);
  });

  it('refuses a path outside the layout before writing anything', async () => {
    process.env.LOCALAI_AICTL_PATH = fake('never', 'exit 0');
    await expect(new AictlLinter().lint([{ path: '../escape.yaml', sha256: 'x', content: 'x' }])).rejects.toThrow(/outside the catalog layout/);
  });

  it('reads the binary path from LOCALAI_AICTL_PATH only', () => {
    expect(aictlPath({ LOCALAI_AICTL_PATH: '/opt/localai/bin/aictl' })).toBe('/opt/localai/bin/aictl');
    expect(aictlPath({})).toBeNull();
    expect(aictlPath({ LOCALAI_AICTL_PATH: '' })).toBeNull();
  });
});
