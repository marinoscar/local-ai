import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { Logger } from '@nestjs/common';

import { candidate, model, node } from '../routing/routing.fixtures';
import { aliasDisplayName, buildModelList, ModelListing, type ModelEntry } from './listing';
import { catalogOf, directoryOf, FakeCatalogStore, FakePolicySource, labNodes, labPolicies, policy, readerOf, tableOf } from './models.fixtures';
import { ModelDirectoryProvider } from './resolve';

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

const FIXTURE = join(__dirname, '../../../../test/fixtures/localai/gateway-models.json');

async function listingOf(rows = labNodes(), policies = labPolicies(), store = new FakeCatalogStore()) {
  const { table, source, clock } = await tableOf(rows);
  const directory = new ModelDirectoryProvider(readerOf(store, clock), new FakePolicySource(policies), clock);
  return { listing: new ModelListing(directory, table), table, source, clock, store };
}

const ids = (entries: ModelEntry[]): string[] => entries.map((entry) => entry.id);
const open = { allowedModels: [] as string[] };

/** Every string anywhere in `value`. */
function strings(value: unknown, into: string[] = []): string[] {
  if (typeof value === 'string') into.push(value);
  else if (Array.isArray(value)) for (const item of value) strings(item, into);
  else if (value && typeof value === 'object') for (const [key, item] of Object.entries(value)) strings(item, strings(key, into));
  return into;
}

describe('ModelListing on the fixture lab (gateway.md section 3.4)', () => {
  it('produces exactly the fixture file', async () => {
    const { listing } = await listingOf();
    expect(await listing.list(open)).toEqual(JSON.parse(readFileSync(FIXTURE, 'utf8')));
  });

  it('lists loaded first, then by id', async () => {
    const { listing } = await listingOf();
    const entries = await listing.build(open);
    expect(entries.map((entry) => [entry.id, entry['x-localai'].state])).toEqual([
      ['lab-coder', 'loaded'],
      ['qwen3-coder-next-q4km', 'loaded'],
      ['qwen3.5-9b-q4km', 'loaded'],
      ['qwen3.6-35b-a3b-q4km', 'loadable'],
    ]);
  });

  it('gives each alias its own entry with alias_of, the metadata of its model and a display name from the alias', async () => {
    const { listing } = await listingOf();
    const entries = await listing.build(open);
    const alias = entries.find((entry) => entry.id === 'lab-coder')!;
    const target = entries.find((entry) => entry.id === 'qwen3-coder-next-q4km')!;
    expect(alias['x-localai']).toEqual({ ...target['x-localai'], display_name: 'Lab coder', alias_of: 'qwen3-coder-next-q4km' });
    expect(target['x-localai'].alias_of).toBeNull();
  });

  it('carries load_wait_s on loadable entries only', async () => {
    const { listing } = await listingOf();
    for (const entry of await listing.build(open)) {
      expect('load_wait_s' in entry['x-localai']).toBe(entry['x-localai'].state === 'loadable');
    }
  });

  it('creates every entry at the approval time of the catalog, the same on every call', async () => {
    const { listing } = await listingOf();
    const first = await listing.build(open);
    const second = await listing.build(open);
    expect(new Set(first.map((entry) => entry.created))).toEqual(new Set([1793620800]));
    expect(second).toEqual(first);
  });

  it('never exposes a host, an address or a tailnet name (recursive)', async () => {
    const rows = labNodes().map((row, index) => ({ ...row, tailnetIp: `100.64.0.${index + 1}`, tailnetMachine: `machine-${index}.tailnet-alpha.ts.net` }));
    const { listing } = await listingOf(rows);
    const body = await listing.list(open);
    const everything = strings(body).join('\n');
    expect(everything).not.toMatch(/example\.test/);
    expect(everything).not.toMatch(/tailnet|\.ts\.net|machine-/i);
    expect(everything).not.toMatch(/\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b/);
    expect(JSON.stringify(body)).not.toMatch(/publicHost|tailnetIp/);
  });
});

describe('ModelListing: what is requestable now', () => {
  it('does not list a loadable model whose policy has no on-demand load', async () => {
    const { listing } = await listingOf(labNodes(), new Map([['qwen3-coder-next-q4km', policy({ aliases: ['lab-coder'] })]]));
    expect(ids(await listing.build(open))).toEqual(['lab-coder', 'qwen3-coder-next-q4km', 'qwen3.5-9b-q4km']);
  });

  it('lists a loadable model once on-demand is on (the default policy keeps it off)', async () => {
    const withOnDemand = new Map([['qwen3.5-4b-q4km', policy({ onDemandLoad: true, loadWaitS: 45 })]]);
    const { listing } = await listingOf(labNodes(), withOnDemand);
    const entry = (await listing.build(open)).find((candidate) => candidate.id === 'qwen3.5-4b-q4km');
    expect(entry?.['x-localai']).toMatchObject({ state: 'loadable', nodes: ['ai-n2'], load_wait_s: 45 });
  });

  it('a model loaded on one node and loadable on another is loaded, naming the loaded node only', async () => {
    const rows = [node('ai-n1', [model('qwen3.5-9b-q4km')]), node('ai-n2', [model('qwen3.5-9b-q4km', { status: 'unloaded' })])];
    const { listing } = await listingOf(rows, new Map([['qwen3.5-9b-q4km', policy({ onDemandLoad: true })]]));
    const entry = (await listing.build(open)).find((candidate) => candidate.id === 'qwen3.5-9b-q4km');
    expect(entry?.['x-localai']).toMatchObject({ state: 'loaded', nodes: ['ai-n1'] });
    expect('load_wait_s' in entry!['x-localai']).toBe(false);
  });

  it('takes the largest context across the nodes, and the catalog context when no node reports one', async () => {
    const rows = [node('ai-n1', [model('qwen3.5-9b-q4km', { ctx: 8192 })]), node('ai-n2', [model('qwen3.5-9b-q4km', { ctx: 32768 })]), node('ai-n3', [model('qwen3.5-4b-q4km', { ctx: null })])];
    const { listing } = await listingOf(rows, new Map());
    const entries = await listing.build(open);
    expect(entries.find((entry) => entry.id === 'qwen3.5-9b-q4km')?.['x-localai']).toMatchObject({ context_length: 32768, nodes: ['ai-n1', 'ai-n2'] });
    expect(entries.find((entry) => entry.id === 'qwen3.5-4b-q4km')?.['x-localai'].context_length).toBe(32768); // llama.ctx of the catalog
  });

  it('reports the input modalities the nodes report, text first, defaulting to text', async () => {
    const rows = [
      node('ai-n1', [model('qwen3.5-9b-q4km', { inputModalities: ['image', 'text'] })]),
      node('ai-n2', [model('qwen3.5-9b-q4km', { inputModalities: ['audio'] })]),
      node('ai-n3', [model('qwen3.5-4b-q4km')]),
    ];
    const { listing } = await listingOf(rows, new Map());
    const entries = await listing.build(open);
    expect(entries.find((entry) => entry.id === 'qwen3.5-9b-q4km')?.['x-localai'].input_modalities).toEqual(['text', 'image', 'audio']);
    expect(entries.find((entry) => entry.id === 'qwen3.5-4b-q4km')?.['x-localai'].input_modalities).toEqual(['text']);
  });

  it('hides a model whose only nodes are suspect, and brings it back when the suspicion ends', async () => {
    const { listing, table, clock } = await listingOf();
    table.markSuspect({ nodeId: 'ai-n3', modelId: 'qwen3.5-9b-q4km' });
    expect(ids(await listing.build(open))).not.toContain('qwen3.5-9b-q4km');
    clock.advance(30_001);
    expect(ids(await listing.build(open))).toContain('qwen3.5-9b-q4km');
  });

  it('hides a model whose only node has an invalid key', async () => {
    const { listing, table } = await listingOf();
    table.markKeyInvalid({ nodeId: 'ai-n1' });
    expect(ids(await listing.build(open))).toEqual(['qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km']);
  });

  it('leaves a suspect node out of the nodes list but keeps the model while a healthy node serves it', async () => {
    const rows = [node('ai-n1', [model('qwen3.5-9b-q4km')]), node('ai-n2', [model('qwen3.5-9b-q4km')])];
    const { listing, table } = await listingOf(rows, new Map());
    table.markSuspect({ nodeId: 'ai-n1', modelId: 'qwen3.5-9b-q4km' });
    expect((await listing.build(open))[0]!['x-localai'].nodes).toEqual(['ai-n2']);
  });

  it('a model marked unloaded moves to loadable (or disappears without on-demand)', async () => {
    const { listing, table } = await listingOf(labNodes(), new Map([['qwen3.5-9b-q4km', policy({ onDemandLoad: true })]]));
    table.markUnloaded('ai-n3', 'qwen3.5-9b-q4km');
    expect((await listing.build(open)).find((entry) => entry.id === 'qwen3.5-9b-q4km')?.['x-localai'].state).toBe('loadable');
  });

  it('a node that is not routable contributes nothing', async () => {
    const rows = [node('ai-n1', [model('qwen3.5-9b-q4km')], { keyReportedAt: null }), node('ai-n2', [model('qwen3.5-4b-q4km')], { gatewayEnabled: false })];
    const { listing } = await listingOf(rows, new Map());
    expect(await listing.build(open)).toEqual([]);
  });

  it('lists only models of the approved catalog', async () => {
    const rows = [node('ai-n1', [model('qwen3.5-9b-q4km'), model('stale-model-gone')])];
    const { listing } = await listingOf(rows, new Map());
    expect(ids(await listing.build(open))).toEqual(['qwen3.5-9b-q4km']);
  });

  it('a disabled policy hides the model and its aliases', async () => {
    const { listing } = await listingOf(labNodes(), new Map([['qwen3-coder-next-q4km', policy({ enabled: false, aliases: ['lab-coder'] })]]));
    expect(ids(await listing.build(open))).toEqual(['qwen3.5-9b-q4km']);
  });

  it('an empty list when no catalog is approved', async () => {
    const { listing } = await listingOf(labNodes(), labPolicies(), new FakeCatalogStore(null));
    expect(await listing.list(open)).toEqual({ object: 'list', data: [] });
    expect(await listing.find(open, 'qwen3.5-9b-q4km')).toBeNull();
  });

  it('an empty list for a lab with no routable node', async () => {
    const { listing } = await listingOf([], labPolicies());
    expect(await listing.list(open)).toEqual({ object: 'list', data: [] });
  });

  it('follows a new approved catalog after the head TTL', async () => {
    const { listing, store, clock } = await listingOf();
    expect((await listing.build(open)).length).toBe(4);
    store.head_ = { version: '2026.11.09-1', approvedAt: new Date('2026-11-09T00:00:00.000Z') };
    store.yaml = 'models:\n  - id: qwen3.5-9b-q4km\n    display_name: "Nine B"\n    roles: [chat]\n';
    clock.advance(6_000);
    const entries = await listing.build(open);
    expect(ids(entries)).toEqual(['qwen3.5-9b-q4km']);
    expect(entries[0]).toMatchObject({ created: Math.floor(new Date('2026-11-09T00:00:00.000Z').getTime() / 1000), 'x-localai': { display_name: 'Nine B', max_output_tokens: null, reasoning: false } });
  });
});

describe('ModelListing: the key scope', () => {
  type Case = [string, string[], string[]];
  const cases: Case[] = [
    ['an empty scope sees everything', [], ['lab-coder', 'qwen3-coder-next-q4km', 'qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km']],
    ['one id sees only that entry', ['qwen3.5-9b-q4km'], ['qwen3.5-9b-q4km']],
    ['a catalog id allows its aliases', ['qwen3-coder-next-q4km'], ['lab-coder', 'qwen3-coder-next-q4km']],
    ['an alias alone does not expose the underlying id', ['lab-coder'], ['lab-coder']],
    ['mixed', ['lab-coder', 'qwen3.6-35b-a3b-q4km'], ['lab-coder', 'qwen3.6-35b-a3b-q4km']],
    ['an unknown entry sees nothing', ['not-a-model'], []],
  ];
  it.each(cases)('%s', async (_name, allowedModels, expected) => {
    const { listing } = await listingOf();
    expect(ids(await listing.build({ allowedModels }))).toEqual(expected);
  });

  it('find() answers null for an entry outside the scope, even when it exists', async () => {
    const { listing } = await listingOf();
    const scoped = { allowedModels: ['qwen3.5-9b-q4km'] };
    expect((await listing.find(scoped, 'qwen3.5-9b-q4km'))?.id).toBe('qwen3.5-9b-q4km');
    expect(await listing.find(scoped, 'lab-coder')).toBeNull();
    expect(await listing.find(scoped, 'qwen3-coder-next-q4km')).toBeNull();
    expect(await listing.find(open, 'lab-coder')).toMatchObject({ id: 'lab-coder' });
    expect(await listing.find(open, 'unknown')).toBeNull();
  });
});

describe('buildModelList and aliasDisplayName', () => {
  it('is empty for a directory without a catalog', () => {
    expect(buildModelList(directoryOf(labPolicies(), null), () => [], [])).toEqual([]);
  });

  it('skips an alias that the index gave to another model', () => {
    const directory = directoryOf(new Map([['qwen3.5-4b-q4km', policy({ aliases: ['shared'] })], ['qwen3.5-9b-q4km', policy({ aliases: ['shared'] })]]), catalogOf());
    const loaded = (id: string) => [candidate('ai-n1', { modelId: id })];
    const list = buildModelList(directory, loaded, []).filter((entry) => ['qwen3.5-4b-q4km', 'qwen3.5-9b-q4km', 'shared'].includes(entry.id));
    expect(list.map((entry) => [entry.id, entry['x-localai'].alias_of])).toEqual([
      ['qwen3.5-4b-q4km', null],
      ['qwen3.5-9b-q4km', null],
      ['shared', 'qwen3.5-4b-q4km'], // the lowest model id owns a duplicated alias
    ]);
  });

  const names: Array<[string, string]> = [
    ['lab-coder', 'Lab coder'],
    ['fast_chat', 'Fast chat'],
    ['gpt-like', 'Gpt like'],
    ['x', 'X'],
    ['--', '--'],
  ];
  it.each(names)('%s -> %s', (alias, expected) => {
    expect(aliasDisplayName(alias)).toBe(expected);
  });
});
