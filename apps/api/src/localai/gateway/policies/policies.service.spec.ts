// The policy schema, the page's rows and the upsert rules, over the fixture lab of GW-05 and an in-memory store. The same
// queries against real SQL are test/localai/gateway-policies.db.spec.ts; the routes, test/localai/gateway-policies-routes.spec.ts.
import { BadRequestException, ConflictException, Logger, NotFoundException } from '@nestjs/common';

import { ApprovedCatalogReader } from '../models/catalog-hints';
import { buildModelList } from '../models/listing';
import { APPROVED_AT, catalogOf, directoryOf, FakeCatalogStore, FakePolicySource, labNodes, labPolicies, tableOf } from '../models/models.fixtures';
import { ModelDirectoryProvider } from '../models/resolve';
import { FakeClock } from '../routing/routing.fixtures';
import { buildModelRows } from './model-rows';
import { parseWith, PutPolicyBodySchema, type PutPolicyBody } from './policies.schemas';
import { changedFields, PoliciesService } from './policies.service';
import type { PolicyData, PolicyRecord, PolicyStore } from './policy.store';

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'error').mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

const valid: PutPolicyBody = { enabled: true, aliases: ['lab-coder'], onDemandLoad: false, loadWaitS: 120, firstByteTimeoutS: null, preferredNodes: ['ai-n1'] };

describe('PutPolicyBodySchema', () => {
  it('accepts the documented body', () => {
    expect(PutPolicyBodySchema.parse(valid)).toEqual(valid);
  });

  const bad: Array<[string, unknown]> = [
    ['a missing field', { ...valid, enabled: undefined }],
    ['an extra field', { ...valid, extra: 1 }],
    ['loadWaitS below 10', { ...valid, loadWaitS: 9 }],
    ['loadWaitS above 600', { ...valid, loadWaitS: 601 }],
    ['a fractional loadWaitS', { ...valid, loadWaitS: 10.5 }],
    ['firstByteTimeoutS 0', { ...valid, firstByteTimeoutS: 0 }],
    ['a fractional firstByteTimeoutS', { ...valid, firstByteTimeoutS: 1.5 }],
    ['an alias that is not a model id shape', { ...valid, aliases: ['Lab Coder'] }],
    ['an alias that is too short', { ...valid, aliases: ['ab'] }],
    ['an alias listed twice', { ...valid, aliases: ['lab-coder', 'lab-coder'] }],
    ['a node id that is not ai-nN', { ...valid, preferredNodes: ['node-1'] }],
    ['a node listed twice', { ...valid, preferredNodes: ['ai-n1', 'ai-n1'] }],
    ['more than 16 aliases', { ...valid, aliases: Array.from({ length: 17 }, (_, i) => `alias-${i}`) }],
  ];
  it.each(bad)('rejects %s', (_name, body) => {
    expect(PutPolicyBodySchema.safeParse(body).success).toBe(false);
  });

  it('accepts the edges: 10 and 600 seconds, a first-byte timeout, no aliases and no nodes', () => {
    expect(PutPolicyBodySchema.safeParse({ ...valid, loadWaitS: 10, firstByteTimeoutS: 1, aliases: [], preferredNodes: [] }).success).toBe(true);
    expect(PutPolicyBodySchema.safeParse({ ...valid, loadWaitS: 600, firstByteTimeoutS: 3600 }).success).toBe(true);
  });

  it('parseWith turns a failure into a 400 with the issues', () => {
    expect(() => parseWith(PutPolicyBodySchema, { ...valid, loadWaitS: 1 })).toThrow(BadRequestException);
  });
});

describe('buildModelRows (the Models page)', () => {
  async function rows(admin: boolean, policies = labPolicies()) {
    const { table } = await tableOf(labNodes());
    return buildModelRows(directoryOf(policies), (id) => table.healthyCandidates(id), () => undefined, admin);
  }

  it('an administrator sees every catalog model; the others only what /v1/models lists', async () => {
    const admin = await rows(true);
    expect(admin.map((row) => row.modelId)).toEqual(['qwen3-coder-next-q4km', 'qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km', 'catalog-only-model', 'qwen3.5-4b-q4km', 'whisper-small']);
    const user = await rows(false);
    expect(user.map((row) => row.modelId)).toEqual(['qwen3-coder-next-q4km', 'qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km']);
  });

  it('carries the state, the nodes and the policy', async () => {
    const byId = new Map((await rows(true)).map((row) => [row.modelId, row]));
    expect(byId.get('qwen3-coder-next-q4km')).toMatchObject({ state: 'loaded', listed: true, nodes: [{ nodeId: 'ai-n1', status: 'loaded' }], policy: { aliases: ['lab-coder'], stored: false } });
    expect(byId.get('qwen3.6-35b-a3b-q4km')).toMatchObject({ state: 'loadable', listed: true, nodes: [{ nodeId: 'ai-n2', status: 'loadable' }], policy: { onDemandLoad: true, loadWaitS: 120 } });
    // present on a node but on-demand is off: not listed, but the page says why
    expect(byId.get('qwen3.5-4b-q4km')).toMatchObject({ state: 'unloaded', listed: false });
    expect(byId.get('catalog-only-model')).toMatchObject({ state: 'none', listed: false, nodes: [] });
  });

  it('a disabled model is not listed whatever its state', async () => {
    const policies = labPolicies();
    policies.set('qwen3.5-9b-q4km', { ...labPolicies().get('qwen3-coder-next-q4km')!, aliases: [], enabled: false });
    const row = (await rows(true, policies)).find((entry) => entry.modelId === 'qwen3.5-9b-q4km')!;
    expect(row).toMatchObject({ state: 'loaded', listed: false });
    expect((await rows(false, policies)).map((entry) => entry.modelId)).not.toContain('qwen3.5-9b-q4km');
  });

  it('agrees with /v1/models: exactly the listed rows are the ids the listing returns', async () => {
    const { table } = await tableOf(labNodes());
    const directory = directoryOf();
    const listed = buildModelRows(directory, (id) => table.healthyCandidates(id), () => undefined, true).filter((row) => row.listed).map((row) => row.modelId);
    const ids = new Set(buildModelList(directory, (id) => table.healthyCandidates(id), []).map((entry) => entry['x-localai'].alias_of ?? entry.id));
    expect(new Set(listed)).toEqual(ids);
  });

  it('is empty when no catalog is approved', async () => {
    const { table } = await tableOf(labNodes());
    expect(buildModelRows(directoryOf(labPolicies(), null), (id) => table.healthyCandidates(id), () => undefined, true)).toEqual([]);
  });
});

describe('changedFields', () => {
  const base: PolicyData = { enabled: true, aliases: ['a-b'], onDemandLoad: false, loadWaitS: 120, firstByteTimeoutS: null, preferredNodes: ['ai-n1'] };
  it('names what differs', () => {
    expect(changedFields(null, base)).toEqual(['created']);
    expect(changedFields(base, base)).toEqual([]);
    expect(changedFields(base, { ...base, enabled: false, aliases: [], onDemandLoad: true, loadWaitS: 30, firstByteTimeoutS: 5, preferredNodes: [] })).toEqual([
      'enabled',
      'aliases',
      'onDemandLoad',
      'loadWaitS',
      'firstByteTimeoutS',
      'preferredNodes',
    ]);
  });
});

class MemoryPolicyStore implements PolicyStore {
  rows = new Map<string, PolicyRecord>();
  async list() {
    return [...this.rows.values()];
  }
  async upsert(modelId: string, data: PolicyData, updatedById: string | null, guard: (others: readonly PolicyRecord[], before: PolicyRecord | null) => void) {
    const before = this.rows.get(modelId) ?? null;
    guard([...this.rows.values()].filter((row) => row.modelId !== modelId), before);
    const after: PolicyRecord = { modelId, ...data, updatedById, updatedAt: new Date('2026-11-04T12:00:00.000Z') };
    this.rows.set(modelId, after);
    return { before, after };
  }
}

async function serviceOver(store = new MemoryPolicyStore(), catalog = true) {
  const { table } = await tableOf(labNodes());
  const clock = new FakeClock();
  const reader = new ApprovedCatalogReader(new FakeCatalogStore(catalog ? undefined : null), clock);
  const directory = new ModelDirectoryProvider(reader, new FakePolicySource(new Map()), clock);
  const invalidate = jest.spyOn(directory, 'invalidate');
  const audit = { record: jest.fn(async () => undefined) };
  const published: Array<{ channel: string; payload: unknown }> = [];
  const bus = { publish: jest.fn(async (channel: string, payload: unknown) => void published.push({ channel, payload })), subscribe: jest.fn() };
  const service = new PoliciesService(store, directory, table, audit as never, bus as never);
  return { service, store, audit, published, invalidate };
}

const admin = { userId: 'user-admin', permissions: ['gateway:admin', 'gateway:use'] };

describe('PoliciesService.upsert', () => {
  it('stores the policy, drops the caches, publishes the event and audits localai.gateway.policy_updated', async () => {
    const { service, store, audit, published, invalidate } = await serviceOver();
    const row = await service.upsert(admin, 'qwen3-coder-next-q4km', valid);
    expect(store.rows.get('qwen3-coder-next-q4km')).toMatchObject({ aliases: ['lab-coder'], preferredNodes: ['ai-n1'], updatedById: 'user-admin' });
    expect(row).toMatchObject({ modelId: 'qwen3-coder-next-q4km', policy: { aliases: ['lab-coder'], stored: true } });
    expect(invalidate).toHaveBeenCalled();
    expect(published).toEqual([{ channel: 'localai.gateway.policy.updated', payload: { modelId: 'qwen3-coder-next-q4km' } }]);
    expect(audit.record).toHaveBeenCalledWith({
      action: 'localai.gateway.policy_updated',
      actorUserId: 'user-admin',
      targetType: 'localai_model_policy',
      targetId: 'qwen3-coder-next-q4km',
      meta: expect.objectContaining({ changed: 'created', aliases: 'lab-coder', preferredNodes: 'ai-n1', enabled: true }),
    });
  });

  it('audits only what changed, and nothing when nothing did', async () => {
    const { service, audit } = await serviceOver();
    await service.upsert(admin, 'qwen3.5-9b-q4km', valid);
    audit.record.mockClear();
    await service.upsert(admin, 'qwen3.5-9b-q4km', { ...valid, onDemandLoad: true });
    expect(audit.record).toHaveBeenCalledWith(expect.objectContaining({ meta: expect.objectContaining({ changed: 'onDemandLoad' }) }));
    audit.record.mockClear();
    await service.upsert(admin, 'qwen3.5-9b-q4km', { ...valid, onDemandLoad: true });
    expect(audit.record).not.toHaveBeenCalled();
  });

  it('404 for a model that is not in the approved catalog, and when no catalog is approved', async () => {
    const { service, store } = await serviceOver();
    await expect(service.upsert(admin, 'no-such-model', valid)).rejects.toBeInstanceOf(NotFoundException);
    expect(store.rows.size).toBe(0);
    const none = await serviceOver(new MemoryPolicyStore(), false);
    await expect(none.service.upsert(admin, 'qwen3.5-9b-q4km', valid)).rejects.toBeInstanceOf(NotFoundException);
  });

  it('400 for an alias equal to a catalog id (the model itself or another), and nothing is written', async () => {
    const { service, store, audit } = await serviceOver();
    for (const alias of ['qwen3.5-9b-q4km', 'whisper-small']) {
      await expect(service.upsert(admin, 'qwen3.5-9b-q4km', { ...valid, aliases: ['fine-one', alias] })).rejects.toBeInstanceOf(BadRequestException);
    }
    expect(store.rows.size).toBe(0);
    expect(audit.record).not.toHaveBeenCalled();
  });

  it('409 for an alias another model owns; the owner may keep it, and a model may re-save its own', async () => {
    const { service, store } = await serviceOver();
    await service.upsert(admin, 'qwen3-coder-next-q4km', valid);
    await expect(service.upsert(admin, 'qwen3.5-9b-q4km', { ...valid, aliases: ['lab-coder'] })).rejects.toBeInstanceOf(ConflictException);
    expect(store.rows.has('qwen3.5-9b-q4km')).toBe(false);
    await expect(service.upsert(admin, 'qwen3-coder-next-q4km', { ...valid, enabled: false })).resolves.toMatchObject({ policy: { enabled: false } });
    // once released, the alias is free
    await service.upsert(admin, 'qwen3-coder-next-q4km', { ...valid, aliases: [] });
    await expect(service.upsert(admin, 'qwen3.5-9b-q4km', { ...valid, aliases: ['lab-coder'] })).resolves.toMatchObject({ policy: { aliases: ['lab-coder'] } });
  });

  it('400 for an alias equal to the id of another stored policy even if the catalog dropped that model', async () => {
    const store = new MemoryPolicyStore();
    store.rows.set('retired-model', { modelId: 'retired-model', enabled: true, aliases: [], onDemandLoad: false, loadWaitS: 120, firstByteTimeoutS: null, preferredNodes: [], updatedById: null, updatedAt: APPROVED_AT });
    const { service } = await serviceOver(store);
    await expect(service.upsert(admin, 'qwen3.5-9b-q4km', { ...valid, aliases: ['retired-model'] })).rejects.toBeInstanceOf(BadRequestException);
  });

  it('a failing audit or event never undoes the write', async () => {
    const { service, store, audit } = await serviceOver();
    audit.record.mockRejectedValue(new Error('audit down'));
    await expect(service.upsert(admin, 'qwen3.5-9b-q4km', valid)).resolves.toBeDefined();
    expect(store.rows.has('qwen3.5-9b-q4km')).toBe(true);
  });
});

describe('PoliciesService.list', () => {
  it('shows the stored policy and its updatedAt', async () => {
    const { service } = await serviceOver();
    await service.upsert(admin, 'qwen3.5-9b-q4km', { ...valid, aliases: [] });
    const rows = await service.list(admin);
    expect(rows.find((row) => row.modelId === 'qwen3.5-9b-q4km')!.policy).toMatchObject({ stored: true, updatedAt: '2026-11-04T12:00:00.000Z' });
    expect(catalogOf().models.size).toBe(rows.length);
  });
});
