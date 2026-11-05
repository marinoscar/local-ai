import { GatewayError } from './errors';
import { CatalogModelResolver } from './model-resolver.catalog';
import { catalogOf, directoryOf, labPolicies, policy } from './models/models.fixtures';
import { ModelResolver, type ModelDirectory, type ModelDirectoryProvider } from './models/resolve';
import { makeKey, makeToken } from './gateway.testkit';

const resolverOver = (directory: ModelDirectory): CatalogModelResolver =>
  new CatalogModelResolver(new ModelResolver({ load: async () => directory } as unknown as ModelDirectoryProvider));
const key = (allowedModels: string[] = []) => makeKey(makeToken(), { allowedModels });
const codeOf = async (promise: Promise<unknown>): Promise<string | null> => promise.then(() => null, (error: unknown) => (error instanceof GatewayError ? error.localaiCode : 'other'));

describe('CatalogModelResolver (GW-05 resolve.ts behind the proxy port)', () => {
  it('resolves an alias to the catalog id with its policy and the catalog roles', async () => {
    const apiKey = key();
    const resolved = await resolverOver(directoryOf(new Map([['qwen3-coder-next-q4km', policy({ aliases: ['lab-coder'], preferredNodes: ['ai-n1'], firstByteTimeoutS: 60 })]]))).resolve('lab-coder', apiKey);
    expect(resolved).toEqual({
      modelId: 'qwen3-coder-next-q4km',
      requestedModel: 'lab-coder',
      policy: { preferredNodes: ['ai-n1'], onDemandLoad: false, loadWaitS: 120, firstByteTimeoutS: 60 },
      roles: ['coder', 'chat'],
      inApprovedCatalog: true,
      // The display prefix only: the on-demand load audits it with the command it queues.
      keyPrefix: apiKey.tokenPrefix,
    });
  });

  it('answers 404 LAI-GW-020 for an unknown or disabled model and without an approved catalog', async () => {
    expect(await codeOf(resolverOver(directoryOf()).resolve('nope', key()))).toBe('LAI-GW-020');
    const disabled = directoryOf(new Map([['qwen3.5-4b-q4km', policy({ enabled: false })]]));
    expect(await codeOf(resolverOver(disabled).resolve('qwen3.5-4b-q4km', key()))).toBe('LAI-GW-020');
    expect(await codeOf(resolverOver(directoryOf(labPolicies(), null)).resolve('qwen3.5-9b-q4km', key()))).toBe('LAI-GW-020');
  });

  it('answers 403 LAI-GW-012 outside the key scope, with the scope rule of section 4.4', async () => {
    const r = resolverOver(directoryOf());
    expect(await codeOf(r.resolve('qwen3.5-9b-q4km', key(['qwen3.5-4b-q4km'])))).toBe('LAI-GW-012');
    // An id entry allows the id and its aliases; an alias entry allows only the alias.
    expect(await codeOf(r.resolve('lab-coder', key(['qwen3-coder-next-q4km'])))).toBeNull();
    expect(await codeOf(r.resolve('lab-coder', key(['lab-coder'])))).toBeNull();
    expect(await codeOf(r.resolve('qwen3-coder-next-q4km', key(['lab-coder'])))).toBe('LAI-GW-012');
  });

  it('reports no roles as unknown, never as "none"', async () => {
    const yaml = 'schema_version: 1\nmodels:\n  - id: bare-model\n    display_name: "Bare"\n    engine: llama\n';
    const resolved = await resolverOver(directoryOf(new Map(), catalogOf(yaml))).resolve('bare-model', key());
    expect(resolved.roles).toBeNull();
  });
});
