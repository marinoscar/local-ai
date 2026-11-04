import { Logger } from '@nestjs/common';

import { GatewayError } from '../errors';
import { FakeClock } from '../routing/routing.fixtures';
import { assertKeyAllows, buildDirectory, keyAllows, ModelDirectoryProvider, ModelResolver, POLICY_TTL_MS, resolveModel, scopeAllows } from './resolve';
import { catalogOf, directoryOf, FakeCatalogStore, FakePolicySource, labPolicies, policy, readerOf } from './models.fixtures';

beforeAll(() => jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined));
afterAll(() => jest.restoreAllMocks());

describe('resolveModel', () => {
  type Case = [string, string, { id: string; alias: string | null } | null];
  const cases: Case[] = [
    ['a catalog id', 'qwen3.5-9b-q4km', { id: 'qwen3.5-9b-q4km', alias: null }],
    ['an alias', 'lab-coder', { id: 'qwen3-coder-next-q4km', alias: 'lab-coder' }],
    ['the id behind an alias still resolves by itself', 'qwen3-coder-next-q4km', { id: 'qwen3-coder-next-q4km', alias: null }],
    ['an unknown name', 'nope', null],
    ['a name that differs only in case', 'Lab-Coder', null],
    ['the empty name', '', null],
  ];
  it.each(cases)('%s', (_name, requested, expected) => {
    const resolved = resolveModel(directoryOf(), requested);
    expect(resolved ? { id: resolved.id, alias: resolved.alias } : null).toEqual(expected);
  });

  it('carries the policy and the catalog hints', () => {
    const resolved = resolveModel(directoryOf(), 'qwen3.6-35b-a3b-q4km');
    expect(resolved?.policy).toMatchObject({ onDemandLoad: true, loadWaitS: 120 });
    expect(resolved?.hints.displayName).toBe('Qwen 3.6 35B A3B');
  });

  it('a model without a policy row is enabled with the defaults', () => {
    expect(resolveModel(directoryOf(), 'qwen3.5-4b-q4km')?.policy).toMatchObject({ enabled: true, onDemandLoad: false, aliases: [] });
  });

  it('a disabled policy hides the model and its aliases', () => {
    const directory = directoryOf(new Map([['qwen3-coder-next-q4km', policy({ enabled: false, aliases: ['lab-coder'] })]]));
    expect(resolveModel(directory, 'qwen3-coder-next-q4km')).toBeNull();
    expect(resolveModel(directory, 'lab-coder')).toBeNull();
  });

  it('a policy of a model that is not in the approved catalog resolves nothing', () => {
    const directory = directoryOf(new Map([['ghost-model', policy({ aliases: ['ghost'] })]]));
    expect(resolveModel(directory, 'ghost-model')).toBeNull();
    expect(resolveModel(directory, 'ghost')).toBeNull();
  });

  it('nothing resolves without an approved catalog', () => {
    expect(resolveModel(directoryOf(labPolicies(), null), 'qwen3.5-9b-q4km')).toBeNull();
    expect(resolveModel(directoryOf(labPolicies(), null), 'lab-coder')).toBeNull();
  });

  it('a catalog id wins over an alias spelled the same, and a duplicated alias is deterministic', () => {
    const clash = buildDirectory(
      catalogOf(),
      new Map([
        ['qwen3.5-4b-q4km', policy({ aliases: ['qwen3.5-9b-q4km', 'shared'] })],
        ['qwen3.5-9b-q4km', policy({ aliases: ['shared'] })],
      ]),
    );
    expect(resolveModel(clash, 'qwen3.5-9b-q4km')?.alias).toBeNull();
    expect(resolveModel(clash, 'shared')?.id).toBe('qwen3.5-4b-q4km'); // the lowest id owns it
  });
});

describe('the scope rule (gateway.md section 4.4)', () => {
  const resolved = (requested: string) => resolveModel(directoryOf(), requested)!;
  type Case = [string, string[], string, boolean];
  const cases: Case[] = [
    ['an empty scope allows an id', [], 'qwen3.5-9b-q4km', true],
    ['an empty scope allows an alias', [], 'lab-coder', true],
    ['the id is allowed by itself', ['qwen3.5-9b-q4km'], 'qwen3.5-9b-q4km', true],
    ['another id is refused', ['qwen3.5-9b-q4km'], 'qwen3.6-35b-a3b-q4km', false],
    ['an alias is refused when only another id is allowed', ['qwen3.5-9b-q4km'], 'lab-coder', false],
    ['allowing the catalog id allows its alias', ['qwen3-coder-next-q4km'], 'lab-coder', true],
    ['allowing the catalog id allows the id', ['qwen3-coder-next-q4km'], 'qwen3-coder-next-q4km', true],
    ['allowing only the alias allows the alias', ['lab-coder'], 'lab-coder', true],
    ['allowing only the alias does NOT allow the underlying id', ['lab-coder'], 'qwen3-coder-next-q4km', false],
    ['a mixed scope', ['lab-coder', 'qwen3.5-9b-q4km'], 'qwen3.5-9b-q4km', true],
  ];
  it.each(cases)('%s', (_name, allowedModels, requested, expected) => {
    expect(keyAllows({ allowedModels }, requested, resolved(requested))).toBe(expected);
    if (expected) expect(() => assertKeyAllows({ allowedModels }, requested, resolved(requested))).not.toThrow();
    else expect(() => assertKeyAllows({ allowedModels }, requested, resolved(requested))).toThrow(expect.objectContaining({ localaiCode: 'LAI-GW-012', status: 403 }));
  });

  it('assertKeyAllows raises a GatewayError of the model_not_allowed row', () => {
    try {
      assertKeyAllows({ allowedModels: ['x-model'] }, 'qwen3.5-9b-q4km', resolved('qwen3.5-9b-q4km'));
      throw new Error('should have thrown');
    } catch (error) {
      expect(error).toBeInstanceOf(GatewayError);
      expect((error as GatewayError).code).toBe('model_not_allowed');
    }
  });

  it('scopeAllows matches by name, or by the catalog id of an alias entry', () => {
    expect(scopeAllows([], 'x', null)).toBe(true);
    expect(scopeAllows(['a'], 'a', null)).toBe(true);
    expect(scopeAllows(['a'], 'b', 'a')).toBe(true);
    expect(scopeAllows(['b'], 'a', 'b')).toBe(true);
    expect(scopeAllows(['b'], 'a', null)).toBe(false);
  });
});

describe('ModelDirectoryProvider and ModelResolver', () => {
  function setup() {
    const store = new FakeCatalogStore();
    const policies = new FakePolicySource();
    const clock = new FakeClock();
    const provider = new ModelDirectoryProvider(readerOf(store, clock), policies, clock);
    return { store, policies, clock, provider, resolver: new ModelResolver(provider) };
  }

  it('resolves an alias through the stored policies', async () => {
    const { resolver } = setup();
    expect(await resolver.resolve('lab-coder')).toMatchObject({ id: 'qwen3-coder-next-q4km', alias: 'lab-coder' });
    expect(await resolver.resolve('unknown')).toBeNull();
  });

  it('reads the policies once per TTL window, and again after invalidate()', async () => {
    const { provider, policies, clock } = setup();
    await provider.load();
    await provider.load();
    expect(policies.reads).toBe(1);
    clock.advance(POLICY_TTL_MS + 1);
    await provider.load();
    expect(policies.reads).toBe(2);
    provider.invalidate();
    await provider.load();
    expect(policies.reads).toBe(3);
  });

  it('keeps serving the last policies when a read fails', async () => {
    const { provider, policies, clock } = setup();
    await provider.load();
    policies.fail = new Error('database down');
    clock.advance(POLICY_TTL_MS + 1);
    expect((await provider.load()).policies.has('qwen3-coder-next-q4km')).toBe(true);
  });
});
