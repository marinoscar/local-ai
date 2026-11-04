import { Logger } from '@nestjs/common';

import { FakeClock } from '../routing/routing.fixtures';
import { CATALOG_HEAD_TTL_MS, parseModelsYaml } from './catalog-hints';
import { APPROVED_AT, APPROVED_SECONDS, FakeCatalogStore, MODELS_YAML, readerOf } from './models.fixtures';

beforeAll(() => jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined));
afterAll(() => jest.restoreAllMocks());

describe('parseModelsYaml', () => {
  it('reads the hints of every model', () => {
    const models = parseModelsYaml(MODELS_YAML);
    expect([...models.keys()]).toEqual(['qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km', 'qwen3-coder-next-q4km', 'qwen3.5-4b-q4km', 'catalog-only-model', 'whisper-small']);
    expect(models.get('qwen3.5-9b-q4km')).toEqual({
      id: 'qwen3.5-9b-q4km',
      displayName: 'Qwen 3.5 9B',
      roles: ['chat', 'reasoning', 'coder'],
      maxOutputTokens: 8192,
      reasoning: true,
      ctx: 65536,
    });
  });

  it('defaults what the catalog leaves out', () => {
    const hints = parseModelsYaml(MODELS_YAML).get('whisper-small');
    expect(hints).toMatchObject({ maxOutputTokens: null, reasoning: false, ctx: null, roles: ['stt'] });
    expect(parseModelsYaml(MODELS_YAML).get('catalog-only-model')).toMatchObject({ maxOutputTokens: null, reasoning: false, ctx: 8192 });
  });

  it('treats max_output_tokens 0 (an embedding model) as no limit hint', () => {
    const yaml = 'models:\n  - id: embed-small\n    client_hints:\n      max_output_tokens: 0\n      reasoning: false\n';
    expect(parseModelsYaml(yaml).get('embed-small')).toMatchObject({ maxOutputTokens: null, reasoning: false, displayName: 'embed-small' });
  });

  const unreadable: Array<[string, string]> = [
    ['empty text', ''],
    ['not YAML', 'models: [unclosed'],
    ['a scalar document', 'just text'],
    ['models is not a list', 'models: {a: 1}'],
  ];
  it.each(unreadable)('gives no models for %s', (_name, text) => {
    expect(parseModelsYaml(text).size).toBe(0);
  });

  it('skips entries without a valid id and repeated ids', () => {
    const yaml = ['models:', '  - display_name: no id', '  - id: Bad_Id', '  - id: ok-model', '    display_name: first', '  - id: ok-model', '    display_name: second', '  - 7', ''].join('\n');
    const models = parseModelsYaml(yaml);
    expect([...models.keys()]).toEqual(['ok-model']);
    expect(models.get('ok-model')?.displayName).toBe('first');
  });
});

describe('ApprovedCatalogReader', () => {
  it('parses once per version and exposes the stable created time', async () => {
    const store = new FakeCatalogStore();
    const clock = new FakeClock();
    const reader = readerOf(store, clock);
    const first = await reader.current();
    expect(first?.version).toBe('2026.11.02-1');
    expect(first?.createdSeconds).toBe(APPROVED_SECONDS);
    expect(first?.approvedAt).toEqual(APPROVED_AT);
    clock.advance(CATALOG_HEAD_TTL_MS + 1);
    const second = await reader.current();
    expect(second).toBe(first); // same parsed object: the head was re-checked, the file was not re-read
    expect(store.heads).toBe(2);
    expect(store.yamls).toBe(1);
  });

  it('does not ask the store again within the TTL', async () => {
    const store = new FakeCatalogStore();
    const reader = readerOf(store, new FakeClock());
    await Promise.all([reader.current(), reader.current(), reader.current()]);
    await reader.current();
    expect(store.heads).toBe(1);
  });

  it('picks up a newly approved version after the TTL, and after invalidate()', async () => {
    const store = new FakeCatalogStore();
    const clock = new FakeClock();
    const reader = readerOf(store, clock);
    await reader.current();
    store.head_ = { version: '2026.11.03-1', approvedAt: new Date('2026-11-03T08:00:00.000Z') };
    expect((await reader.current())?.version).toBe('2026.11.02-1');
    reader.invalidate();
    const next = await reader.current();
    expect(next?.version).toBe('2026.11.03-1');
    expect(next?.createdSeconds).toBe(Math.floor(new Date('2026-11-03T08:00:00.000Z').getTime() / 1000));
    expect(store.yamls).toBe(2);
  });

  it('answers null when no catalog is approved', async () => {
    const store = new FakeCatalogStore(null);
    expect(await readerOf(store, new FakeClock()).current()).toBeNull();
  });

  it('serves the previous catalog when a refresh fails, and throws when there is none', async () => {
    const store = new FakeCatalogStore();
    const clock = new FakeClock();
    const reader = readerOf(store, clock);
    const good = await reader.current();
    store.fail = new Error('database down');
    clock.advance(CATALOG_HEAD_TTL_MS + 1);
    expect(await reader.current()).toBe(good);

    const cold = readerOf(store, new FakeClock());
    await expect(cold.current()).rejects.toThrow('database down');
  });
});
