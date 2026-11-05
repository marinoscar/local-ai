// lab-cache.json (src/main/cloud-api.ts) and the reading of cloud data (src/shared/lab-data.ts): the file is 0600, holds the
// node cards and nothing secret, stays bounded, and is replaced atomically.
import { randomBytes } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { LAB_CACHE_FILE, MAX_CACHE_BYTES, createLabCacheStore, labCachePath, parseLabCache, serializeLabCache } from '../../src/main/cloud-api';
import { MAX_NODES, projectCommand, projectLabNodes, projectModels, projectNodeCard, readRefusal, secretShaped } from '../../src/shared/lab-data';
import fixture from '../fixtures/lab-3-nodes.json';

const dirs: string[] = [];
afterEach(async () => {
  await Promise.all(dirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});
const tmp = async (): Promise<string> => {
  const dir = await mkdtemp(join(tmpdir(), 'lab-cache-'));
  dirs.push(dir);
  return dir;
};
const body = (nodes: unknown[] = fixture.nodes, summary: unknown = fixture.summary) => ({ data: { summary, nodes } });
const at = new Date('2026-11-03T14:05:00.000Z');

// Credentials are built at run time so that no literal in this file looks like one.
const hex = (bytes: number): string => randomBytes(bytes).toString('hex');
// Tailnet addresses are built at run time too (the repo's redaction check scans literals).
const tailnetIp = (second: number, third = 1, fourth = 2): string => ['100', second, third, fourth].join('.');
const token = (prefix: string): string => `${prefix}_${randomBytes(18).toString('base64url')}`;

describe('reading node cards (src/shared/lab-data.ts)', () => {
  it('keeps every field of the cloud fixture: the projection is the identity on a real answer', () => {
    for (const node of fixture.nodes) expect(projectNodeCard(node)).toEqual(node);
    expect(projectLabNodes(body())).toEqual({ summary: fixture.summary, nodes: fixture.nodes });
  });

  it('drops a field the contract does not have, and a card without a valid id', () => {
    const extra = { ...fixture.nodes[0], tailnetIp: tailnetIp(101, 102, 103), apiKey: token('lai'), snapshot: { x: 1 } };
    const card = projectNodeCard(extra)!;
    expect(Object.keys(card).sort()).toEqual(Object.keys(fixture.nodes[0]!).sort());
    expect(JSON.stringify(card)).not.toContain(tailnetIp(101, 102, 103));
    for (const nodeId of ['AI-N1', 'ai-n0', '../x', 7, null, undefined]) expect(projectNodeCard({ ...fixture.nodes[0], nodeId })).toBeNull();
    expect(projectLabNodes(body([...fixture.nodes, { nodeId: 'bad' }, 'x', null]))!.nodes).toHaveLength(3);
  });

  it('turns unknown enumerations into the quiet defaults rather than trusting them', () => {
    const card = projectNodeCard({ ...fixture.nodes[0], health: 'on-fire', phase: 'x', mode: 'party', backend: 'tpu', ark: [{ rootId: 'internal', kind: 'weird', colour: 'purple', label: 'ok', present: 'yes' }] })!;
    expect(card).toMatchObject({ health: 'quiet', phase: 'pending', mode: null, backend: null });
    expect(card.ark[0]).toMatchObject({ kind: 'internal', colour: 'grey', present: false });
  });

  it('removes secret-shaped text from every string field', () => {
    const hardware = `GPU ${token('pat')}`;
    const card = projectNodeCard({ ...fixture.nodes[0], hardware, displayName: `Name ${hex(32)}`, lastDefault: token('nod'), agentVersion: tailnetIp(100) })!;
    expect(card.hardware).toBeNull();
    expect(card.displayName).toBe('ai-n1'); // falls back to the id
    expect(card.lastDefault).toBeNull();
    expect(card.agentVersion).toBeNull();
  });

  it('is bounded: strings, lists and the number of cards', () => {
    const many = Array.from({ length: 200 }, (_v, i) => ({ ...fixture.nodes[0], nodeId: `ai-n${i + 1}`, displayName: 'x'.repeat(1000), ark: Array.from({ length: 50 }, (_w, j) => ({ rootId: `r${j}`, kind: 'internal', colour: 'green', label: 'ok', present: true, driveAgeDays: null })) }));
    const out = projectLabNodes(body(many))!;
    expect(out.nodes).toHaveLength(MAX_NODES);
    expect(out.nodes[0]!.displayName.length).toBeLessThanOrEqual(120);
    expect(out.nodes[0]!.ark.length).toBeLessThanOrEqual(16);
  });

  it.each([null, 'x', 7, {}, { data: null }, { data: { nodes: 'x' } }, { nodes: [] }])('a body that is not the node list (%j) is null', (value) => {
    expect(projectLabNodes(value)).toBeNull();
  });

  it('secretShaped recognises the lab prefixes, bearer headers, 64 hex digits and tailnet addresses, and not ordinary text', () => {
    for (const prefix of ['lat', 'lai', 'nod', 'pat', 'pg']) expect(secretShaped(token(prefix)), prefix).toBe(true);
    expect(secretShaped(`Bearer ${hex(12)}`)).toBe(true);
    expect(secretShaped(hex(32))).toBe(true);
    for (const ip of [tailnetIp(64, 0, 1), tailnetIp(101, 102, 103), tailnetIp(127, 255, 254)]) expect(secretShaped(`at ${ip}`), ip).toBe(true);
    for (const text of ['qwen3.6-35b-a3b-q4km', 'RTX 4080 12 GB', 'Drive not plugged in · last synced 6 days ago', tailnetIp(63), tailnetIp(128), 'lai_x', 'pat', 'platform_version_2', hex(8)]) expect(secretShaped(text), text).toBe(false);
  });

  it('reads a command, the models of a node and a refusal', () => {
    const id = '6b3c1f0e-4a2d-4c8b-9e51-0a7d2b9c3e44';
    expect(projectCommand({ data: { id, nodeId: 'ai-n2', type: 'localai.model.load', status: 'failed', error: { code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' }, secret: 'x' } })).toEqual({
      id,
      nodeId: 'ai-n2',
      type: 'localai.model.load',
      status: 'failed',
      errorCode: 'LAI-CAT-005',
      errorMessage: 'Model does not fit in free VRAM',
    });
    expect(projectCommand({ data: { id: 'x', nodeId: 'ai-n2', status: 'queued' } })).toBeNull();
    expect(projectCommand({ data: { id, nodeId: 'ai-n2', status: 'weird' } })).toBeNull();
    const models = projectModels({ data: { lastState: { models: [{ id: 'qwen3.5-9b-q4km', present: true, loadable: true, status: 'loaded', default: true }, { id: 'Bad Id' }, { id: 'big-model-q8', present: false, loadable: false, status: 'unloaded', default: false }] } } });
    expect(models).toEqual([
      { id: 'qwen3.5-9b-q4km', status: 'loaded', isDefault: true, loadable: true },
      { id: 'big-model-q8', status: 'unloaded', isDefault: false, loadable: false },
    ]);
    expect(projectModels({ data: { lastState: null } })).toEqual([]);
    expect(projectModels('x')).toBeNull();
    expect(readRefusal({ error: { message: 'x', details: { code: 'LAI-CLD-034', reason: 'node_offline_mode', mode: 'survival' } } })).toEqual({ code: 'LAI-CLD-034', reason: 'node_offline_mode', mode: 'survival' });
    expect(readRefusal({ statusCode: 409, message: 'x', details: { code: 'LAI-CLD-035', reason: 'node_retired' } })).toMatchObject({ code: 'LAI-CLD-035' });
    expect(readRefusal({ message: 'plain' })).toEqual({ code: null, reason: null, mode: null });
  });
});

describe('lab-cache.json', () => {
  it('is written 0600 in a 0700 folder, holds the node cards and the time, and reads back', async () => {
    const dir = join(await tmp(), 'ai-desktop');
    const store = createLabCacheStore({ dir });
    expect(await store.save(body(), at)).toBe(true);
    const file = labCachePath(dir);
    expect(file.endsWith(LAB_CACHE_FILE)).toBe(true);
    expect((await stat(file)).mode & 0o777).toBe(0o600);
    expect((await stat(dir)).mode & 0o777).toBe(0o700);
    const onDisk = JSON.parse(await readFile(file, 'utf8')) as Record<string, unknown>;
    expect(Object.keys(onDisk).sort()).toEqual(['nodes', 'savedAt', 'schema', 'summary']);
    expect(onDisk['savedAt']).toBe('2026-11-03T14:05:00.000Z');
    expect(await store.load()).toEqual({ savedAt: '2026-11-03T14:05:00.000Z', summary: fixture.summary, nodes: fixture.nodes });
  });

  it('holds no pat_, nod_, lai_ or 64-hex string, even when the cloud answer carried them', async () => {
    const dir = await tmp();
    const dirty = fixture.nodes.map((n) => ({
      ...n,
      tailnetIp: tailnetIp(99, 88, 77),
      key: token('lai'),
      nodeToken: token('nod'),
      pat: token('pat'),
      fingerprint: hex(32),
      hardware: `${n.hardware} ${token('pat')}`,
      displayName: `${n.displayName} ${hex(32)}`,
    }));
    const store = createLabCacheStore({ dir });
    await store.save(body(dirty), at);
    const text = await readFile(labCachePath(dir), 'utf8');
    for (const pattern of [/pat_/, /nod_/, /lai_/, /lat_/, /pg_/, /\b[0-9a-f]{64}\b/, new RegExp(tailnetIp(99, 88, 77).replaceAll('.', '\\.')), /tailnetIp/, /Bearer/i]) expect(text).not.toMatch(pattern);
    expect(JSON.parse(text).nodes).toHaveLength(3);
  });

  it('serializeLabCache refuses a body it cannot read and a result that would be too big', () => {
    expect(serializeLabCache({ message: 'x' }, at)).toBeNull();
    const huge = Array.from({ length: MAX_NODES }, (_v, i) => ({ ...fixture.nodes[0], nodeId: `ai-n${i + 1}`, ark: Array.from({ length: 16 }, (_w, j) => ({ rootId: `root-${j}`, kind: 'internal', colour: 'green', label: 'x'.repeat(200), present: true, driveAgeDays: null })) }));
    const text = serializeLabCache(body(huge), at);
    expect(text === null || Buffer.byteLength(text) <= MAX_CACHE_BYTES).toBe(true);
  });

  it('a body that cannot be read leaves the earlier cache alone', async () => {
    const dir = await tmp();
    const store = createLabCacheStore({ dir });
    await store.save(body(), at);
    expect(await store.save({ message: 'Service Unavailable' }, new Date())).toBe(false);
    expect((await store.load())?.savedAt).toBe(at.toISOString());
  });

  it('is replaced as a whole, leaves no temporary file, and keeps its mode when it is written again', async () => {
    const dir = await tmp();
    const store = createLabCacheStore({ dir });
    await Promise.all([store.save(body(fixture.nodes.slice(0, 1)), at), store.save(body(fixture.nodes.slice(0, 2)), new Date(at.getTime() + 1000)), store.save(body(), new Date(at.getTime() + 2000))]);
    expect(await readdir(dir)).toEqual([LAB_CACHE_FILE]);
    expect((await store.load())?.nodes).toHaveLength(3);
    expect((await stat(labCachePath(dir))).mode & 0o777).toBe(0o600);
  });

  it('clear() deletes it, and is fine when there is none', async () => {
    const dir = await tmp();
    const store = createLabCacheStore({ dir });
    await store.clear();
    await store.save(body(), at);
    await store.clear();
    expect(await store.load()).toBeNull();
    expect(await readdir(dir)).toEqual([]);
  });

  it('load() is null for a missing file, garbage, another schema, a bad time, an oversized file and a folder', async () => {
    const dir = await tmp();
    const store = createLabCacheStore({ dir });
    expect(await store.load()).toBeNull();
    const file = labCachePath(dir);
    for (const content of ['not json', '[]', JSON.stringify({ schema: 2, savedAt: at.toISOString(), summary: {}, nodes: [] }), JSON.stringify({ schema: 1, savedAt: 'yesterday', summary: {}, nodes: [] }), JSON.stringify({ schema: 1, savedAt: at.toISOString(), summary: {}, nodes: 'x' })]) {
      await writeFile(file, content);
      expect(await store.load(), content).toBeNull();
    }
    await writeFile(file, ' '.repeat(MAX_CACHE_BYTES + 1));
    expect(await store.load()).toBeNull();
    await rm(file);
    await mkdir(file);
    expect(await store.load()).toBeNull();
  });

  it('load() reads a file back through the projection: a hand-edited cache cannot smuggle a secret onto the page', async () => {
    const dir = await tmp();
    const leaked = token('pat');
    await writeFile(labCachePath(dir), JSON.stringify({ schema: 1, savedAt: at.toISOString(), summary: fixture.summary, nodes: [{ ...fixture.nodes[0], hardware: leaked, tailnetIp: tailnetIp(70) }] }));
    const view = await createLabCacheStore({ dir }).load();
    expect(JSON.stringify(view)).not.toContain(leaked);
    expect(JSON.stringify(view)).not.toContain(tailnetIp(70));
  });

  it('parseLabCache and serializeLabCache are inverse on the fixture', () => {
    const text = serializeLabCache(body(), at)!;
    expect(parseLabCache(text)).toEqual({ savedAt: at.toISOString(), summary: fixture.summary, nodes: fixture.nodes });
  });

  it('a write that fails (the folder is a file) answers false and does not throw', async () => {
    const dir = await tmp();
    const blocker = join(dir, 'blocker');
    await writeFile(blocker, 'x');
    expect(await createLabCacheStore({ dir: join(blocker, 'inner') }).save(body(), at)).toBe(false);
  });
});
