import { execFileSync, spawnSync } from 'node:child_process';
import { cp, readFile, stat, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { getCurrentFuseWire, FuseState, FuseV1Options } from '@electron/fuses';
import { afterEach, describe, expect, it } from 'vitest';
import { EXECUTABLE, EXPECTED_FUSES, makeTestCopy, verifyPackagedApp } from '../../scripts/packaged-app.mjs';
import { GOOD_ASAR_FILES, fuseWire, makeFixtureApp, removeFixture } from '../fixtures/fixture-app';

const script = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'scripts', 'packaged-app.mjs');
const made: string[] = [];
const fixture = async (options?: Parameters<typeof makeFixtureApp>[0]) => {
  const app = await makeFixtureApp(options);
  made.push(app);
  return app;
};
afterEach(async () => {
  for (const app of made.splice(0)) await removeFixture(app);
});

describe('packaged-app verify', () => {
  it('passes a correct fixture', async () => {
    const result = await verifyPackagedApp(await fixture());
    expect(result.problems).toEqual([]);
    expect(result.ok).toBe(true);
  });

  it('fails when a fuse is wrong, naming it (runAsNode on)', async () => {
    const app = await fixture({ fuses: { ...EXPECTED_FUSES, [FuseV1Options.RunAsNode]: true } });
    const result = await verifyPackagedApp(app);
    expect(result.ok).toBe(false);
    expect(result.problems).toEqual(['fuse RunAsNode is enabled, expected disabled']);
  });

  it.each(Object.keys(EXPECTED_FUSES).map(Number))('fails when only fuse %i is flipped', async (index) => {
    const app = await fixture({ fuses: { ...EXPECTED_FUSES, [index]: !EXPECTED_FUSES[index] } });
    const result = await verifyPackagedApp(app);
    expect(result.ok).toBe(false);
    expect(result.problems).toHaveLength(1);
    expect(result.problems[0]).toContain(`fuse ${FuseV1Options[index]}`);
  });

  it('fails when a fuse is marked removed', async () => {
    const app = await fixture();
    const binary = join(app, EXECUTABLE);
    const bytes = await readFile(binary);
    const wire = fuseWire({ ...EXPECTED_FUSES });
    const at = bytes.indexOf(wire);
    bytes[at + wire.length - 9] = FuseState.REMOVED; // the first state byte (RunAsNode)
    await writeFile(binary, bytes);
    const result = await verifyPackagedApp(app);
    expect(result.problems).toEqual(['fuse RunAsNode is removed, expected disabled']);
  });

  it('ignores a fuse that electron-builder.yml does not configure (WasmTrapHandlers)', async () => {
    const app = await fixture({ fuses: { ...EXPECTED_FUSES, [FuseV1Options.WasmTrapHandlers]: true } });
    expect((await verifyPackagedApp(app)).ok).toBe(true);
  });

  it('fails on a source map inside the asar', async () => {
    const app = await fixture({ asarFiles: { ...GOOD_ASAR_FILES, 'out/main/index.js.map': '{}' } });
    const result = await verifyPackagedApp(app);
    expect(result.ok).toBe(false);
    expect(result.problems).toEqual(['source map inside the asar: /out/main/index.js.map']);
  });

  it('fails on node_modules/electron inside the asar', async () => {
    const app = await fixture({ asarFiles: { ...GOOD_ASAR_FILES, 'node_modules/electron/index.js': '' } });
    const result = await verifyPackagedApp(app);
    expect(result.ok).toBe(false);
    expect(result.problems[0]).toMatch(/node_modules\/electron inside the asar/);
  });

  it('does not mistake node_modules/electron-something for electron', async () => {
    const app = await fixture({ asarFiles: { ...GOOD_ASAR_FILES, 'node_modules/electron-log/index.js': '' } });
    expect((await verifyPackagedApp(app)).ok).toBe(true);
  });

  it('fails when the app is not packed in an asar', async () => {
    const app = await fixture({ noAsar: true });
    const result = await verifyPackagedApp(app);
    expect(result.ok).toBe(false);
    expect(result.problems.join('\n')).toContain('not packed in an asar');
    expect(result.problems.join('\n')).toContain('app.asar is missing');
  });

  it('fails when the binary is missing', async () => {
    const result = await verifyPackagedApp(await fixture({ noBinary: true }));
    expect(result.ok).toBe(false);
    expect(result.problems).toEqual(['ai-desktop binary is missing']);
  });

  it('fails when the binary carries no fuse wire', async () => {
    const app = await fixture();
    await writeFile(join(app, EXECUTABLE), 'not an electron binary');
    const result = await verifyPackagedApp(app);
    expect(result.ok).toBe(false);
    expect(result.problems[0]).toContain('cannot read the fuses');
  });
});

describe('packaged-app command line', () => {
  it('exits 0 on a good app and 1 on a bad one', async () => {
    const good = await fixture();
    expect(spawnSync(process.execPath, [script, 'verify', good]).status).toBe(0);
    const bad = await fixture({ fuses: { ...EXPECTED_FUSES, [FuseV1Options.OnlyLoadAppFromAsar]: false } });
    const run = spawnSync(process.execPath, [script, 'verify', bad], { encoding: 'utf8' });
    expect(run.status).toBe(1);
    expect(run.stderr).toContain('FAIL fuse OnlyLoadAppFromAsar is disabled, expected enabled');
  });

  it('exits 2 on an unknown command', () => {
    expect(spawnSync(process.execPath, [script, 'nope']).status).toBe(2);
  });

  it('exits 1 when there is nothing to verify', () => {
    expect(spawnSync(process.execPath, [script, 'verify', '/nonexistent/ai-desktop-app']).status).toBe(1);
  });
});

describe('packaged-app test-copy', () => {
  it('re-enables only the inspector fuse in the copy and leaves the original alone', async () => {
    const app = await fixture();
    const to = join(dirname(app), 'test-copy');
    await makeTestCopy(app, to);
    const original = (await getCurrentFuseWire(join(app, EXECUTABLE))) as unknown as Record<number, number>;
    const copy = (await getCurrentFuseWire(join(to, EXECUTABLE))) as unknown as Record<number, number>;
    expect(original[FuseV1Options.EnableNodeCliInspectArguments]).toBe(FuseState.DISABLE);
    expect(copy[FuseV1Options.EnableNodeCliInspectArguments]).toBe(FuseState.ENABLE);
    for (const index of Object.keys(EXPECTED_FUSES).map(Number).filter((i) => i !== FuseV1Options.EnableNodeCliInspectArguments)) {
      expect(copy[index]).toBe(original[index]);
    }
    expect((await stat(join(to, 'resources', 'app.asar'))).isFile()).toBe(true);
    // the copy now fails verify (inspect on): proof it can never pass for a shippable app
    expect((await verifyPackagedApp(to)).ok).toBe(false);
  });

  it('replaces an earlier copy but refuses to delete a folder it did not create', async () => {
    const app = await fixture();
    const to = join(dirname(app), 'test-copy');
    await makeTestCopy(app, to);
    await expect(makeTestCopy(app, to)).resolves.toBe(to);
    const other = join(dirname(app), 'other');
    await cp(app, other, { recursive: true });
    await expect(makeTestCopy(app, other)).rejects.toThrow(/not a test copy/);
  });

  it('refuses a missing source and nested folders', async () => {
    const app = await fixture();
    await expect(makeTestCopy('/nonexistent/ai-desktop-app', join(dirname(app), 'x'))).rejects.toThrow(/no ai-desktop binary/);
    await expect(makeTestCopy(app, join(app, 'inside'))).rejects.toThrow(/separate folders/);
  });

  it('works from the command line', async () => {
    const app = await fixture();
    const to = join(dirname(app), 'cli-copy');
    const out = execFileSync(process.execPath, [script, 'test-copy', '--from', app, '--to', to], { encoding: 'utf8' });
    expect(out).toContain('test-copy: wrote');
  });
});
