import { COMMAND_TYPES, COMMANDS, type CommandType } from '../contracts/commands';
import { LOCALAI_PERMISSION_IDS } from '../localai.permissions';
import { COMMAND_POLICY, dedupKeyFor, policyOf } from './command-policy';
import { readCommandExamples } from '../../../test/fixtures/localai/command-examples';

describe('the command policy table (spec 3.7)', () => {
  it('lists exactly the registry\'s 17 types', () => {
    expect(Object.keys(COMMAND_POLICY).sort()).toEqual([...COMMAND_TYPES].sort());
  });

  // type: [permission, lease s, hard cap s, queued TTL min, dedup subject placeholder]
  const TABLE: Record<CommandType, [string, number, number, number]> = {
    'localai.status.refresh': ['lab:operate', 30, 300, 2],
    'localai.model.load': ['lab:operate', 60, 600, 15],
    'localai.model.unload': ['lab:operate', 30, 300, 15],
    'localai.model.set_default': ['lab:operate', 30, 300, 15],
    'localai.model.download': ['lab:operate', 30, 300, 24 * 60],
    'localai.model.delete': ['lab:admin', 60, 600, 15],
    'localai.ark.sync': ['lab:operate', 30, 300, 24 * 60],
    'localai.ark.verify': ['lab:operate', 30, 300, 24 * 60],
    'localai.mode.offline': ['lab:admin', 30, 300, 10],
    'localai.key.rotate': ['lab:admin', 60, 600, 10],
    'localai.service.restart': ['lab:operate', 60, 600, 10],
    'localai.logs.tail': ['lab:operate', 30, 300, 2],
    'localai.catalog.refresh': ['lab:operate', 60, 600, 60],
    'localai.agent.update': ['lab:admin', 300, 900, 24 * 60],
    'localai.ensure.run': ['lab:operate', 30, 300, 60],
    'localai.workload.activate': ['localai_workloads:write', 60, 600, 15],
    'localai.music.generate': ['localai_music:use', 60, 1800, 15],
  };

  it.each(Object.entries(TABLE))('%s: permission, lease, hard cap and queued TTL match the spec table', (type, [permission, lease, cap, ttl]) => {
    const policy = policyOf(type as CommandType);
    expect(policy.permission).toBe(permission);
    expect(policy.leaseMs).toBe(lease * 1000);
    expect(policy.hardCapMs).toBe(cap * 1000);
    expect(policy.queuedTtlMs).toBe(ttl * 60 * 1000);
    expect(policy.maxAttempts).toBe(1);
  });

  it('every permission is one of the seeded lab permissions, and the four destructive types need lab:admin', () => {
    const ids = new Set<string>(Object.values(LOCALAI_PERMISSION_IDS));
    for (const type of COMMAND_TYPES) expect(ids.has(COMMAND_POLICY[type].permission)).toBe(true);
    const admin = COMMAND_TYPES.filter((type) => COMMAND_POLICY[type].permission === LOCALAI_PERMISSION_IDS.LAB_ADMIN).sort();
    expect(admin).toEqual(['localai.agent.update', 'localai.key.rotate', 'localai.mode.offline', 'localai.model.delete']);
  });

  it('hard cap is the lease times ten, except model.load (600 s), agent.update (900 s) and music.generate (1800 s)', () => {
    for (const type of COMMAND_TYPES) {
      const { leaseMs, hardCapMs } = COMMAND_POLICY[type];
      if (type === 'localai.agent.update') expect(hardCapMs).toBe(900_000);
      else if (type === 'localai.music.generate') expect(hardCapMs).toBe(1_800_000);
      else if (type === 'localai.model.load') expect(hardCapMs).toBe(600_000);
      else expect(hardCapMs).toBe(leaseMs * 10);
      expect(hardCapMs).toBeGreaterThan(leaseMs);
    }
  });

  it('tells the agent to renew inside the lease and inside its own 5 to 60 second clamp', () => {
    for (const type of COMMAND_TYPES) {
      const { leaseMs, renewIntervalMs } = COMMAND_POLICY[type];
      expect(renewIntervalMs).toBeGreaterThanOrEqual(5_000);
      expect(renewIntervalMs).toBeLessThanOrEqual(60_000);
      expect(renewIntervalMs).toBeLessThan(leaseMs);
    }
    expect(COMMAND_POLICY['localai.model.load'].renewIntervalMs).toBe(20_000);
  });

  describe('dedupKeyFor', () => {
    it('is "<type>:<subject>" with the subject the table names', () => {
      expect(dedupKeyFor('localai.model.load', { model_id: 'qwen3.6-35b-a3b-q4km' })).toBe('model.load:qwen3.6-35b-a3b-q4km');
      expect(dedupKeyFor('localai.model.unload', { model_id: 'gemma-4-26b-q4km' })).toBe('model.unload:gemma-4-26b-q4km');
      expect(dedupKeyFor('localai.model.download', { model_id: 'gemma-4-26b-q4km' })).toBe('model.download:gemma-4-26b-q4km');
      expect(dedupKeyFor('localai.model.delete', { model_id: 'gemma-4-26b-q4km' })).toBe('model.delete:gemma-4-26b-q4km');
      expect(dedupKeyFor('localai.service.restart', { service: 'llama' })).toBe('service.restart:llama');
      expect(dedupKeyFor('localai.logs.tail', { unit: 'agent' })).toBe('logs.tail:agent');
      expect(dedupKeyFor('localai.music.generate', { request_id: '5d1c0b7e-2f43-4a51-9a0e-7b6c1d2e3f40' })).toBe('music.generate:5d1c0b7e-2f43-4a51-9a0e-7b6c1d2e3f40');
    });

    it('workload.activate has no subject: one switch at a time per node', () => {
      expect(dedupKeyFor('localai.workload.activate', { name: 'music' })).toBe('workload.activate:-');
    });

    it('uses "-" for a type with no subject: one active command per node', () => {
      for (const type of ['localai.status.refresh', 'localai.model.set_default', 'localai.ark.sync', 'localai.ark.verify', 'localai.mode.offline', 'localai.key.rotate', 'localai.catalog.refresh', 'localai.agent.update'] as const) {
        expect(dedupKeyFor(type, {})).toBe(`${type.replace('localai.', '')}:-`);
      }
    });

    it('can always be built from the parsed example params of its type', () => {
      const examples = readCommandExamples<Record<string, { params: unknown }>>();
      for (const type of COMMAND_TYPES) {
        const params = COMMANDS[type].params.parse(examples[type].params);
        expect(dedupKeyFor(type, params).startsWith(`${type.replace('localai.', '')}:`)).toBe(true);
      }
    });
  });
});
