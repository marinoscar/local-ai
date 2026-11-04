import { randomBytes } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { DEFAULT_MAX_BYTES, createFileLogger, scrub } from '../../src/main/log';

let dir: string;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'ai-desktop-log-'));
});
afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
});

describe('main.log', () => {
  it('writes JSON lines with ts, level, msg, component and code, mode 0600', async () => {
    const log = createFileLogger({ dir, stderr: false, now: () => new Date('2026-11-04T10:00:00Z') });
    log.log('error', 'agent', 'the agent does not answer', { code: 'LAI-DSK-004' });
    const lines = (await readFile(join(dir, 'main.log'), 'utf8')).trim().split('\n');
    expect(JSON.parse(lines[0]!)).toEqual({ ts: '2026-11-04T10:00:00.000Z', level: 'error', msg: 'the agent does not answer', component: 'agent', code: 'LAI-DSK-004' });
    expect((await stat(join(dir, 'main.log'))).mode & 0o777).toBe(0o600);
  });

  it('rotates at 5 MB and keeps three files', async () => {
    expect(DEFAULT_MAX_BYTES).toBe(5 * 1024 * 1024);
    const log = createFileLogger({ dir, stderr: false });
    const chunk = 'x'.repeat(100 * 1024);
    for (let i = 0; i < 130; i += 1) log.log('info', 'app', chunk, { n: i }); // about 13 MB
    expect((await readdir(dir)).sort()).toEqual(['main.log', 'main.log.1', 'main.log.2']);
    for (const name of ['main.log', 'main.log.1', 'main.log.2']) expect((await stat(join(dir, name))).size).toBeLessThanOrEqual(DEFAULT_MAX_BYTES);
    // The newest line is in main.log, the oldest kept ones are in the rotated files.
    const last = (await readFile(join(dir, 'main.log'), 'utf8')).trim().split('\n').at(-1)!;
    expect((JSON.parse(last) as { n: number }).n).toBe(129);
  });

  it('keeps a rotation running across a restart (the size is read from the file)', async () => {
    const first = createFileLogger({ dir, stderr: false, maxBytes: 1000 });
    first.log('info', 'app', 'y'.repeat(600));
    const second = createFileLogger({ dir, stderr: false, maxBytes: 1000 });
    second.log('info', 'app', 'z'.repeat(600));
    expect((await readdir(dir)).sort()).toEqual(['main.log', 'main.log.1']);
  });

  it('never writes a token, bearer header or session cookie', async () => {
    const token = `lat_${randomBytes(32).toString('base64url')}`;
    const log = createFileLogger({ dir, stderr: false });
    log.log('error', 'agent', `login failed with Authorization: Bearer ${token}`, { cookie: `localai_session=${token}`, key: token });
    const text = await readFile(join(dir, 'main.log'), 'utf8');
    expect(text).not.toContain(token);
    expect(text).toContain('[redacted]');
  });

  it('scrub leaves ordinary text alone', () => {
    expect(scrub('the agent answered in 12 ms at 127.0.0.1')).toBe('the agent answered in 12 ms at 127.0.0.1');
  });
});
