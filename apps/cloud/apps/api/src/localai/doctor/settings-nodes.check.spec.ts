import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { expectContract } from '../../../test/localai/support/doctor-asserts';
import { SettingsNodesDoctorCheck } from './settings-nodes.check';

const registry = { register: jest.fn() };
const policy = (over: Record<string, number | boolean> = {}) => ({ staleHeartbeatSeconds: 45, offlineStaleMultiplier: 4, offlineRetentionDays: 3650, jobSecretBrokerEnabled: false, ...over });
const checkWith = (value: Record<string, number | boolean>) =>
  new SettingsNodesDoctorCheck(registry as never, { getNamespace: jest.fn(async () => value) } as never);

describe('localai.settings.nodes', () => {
  it('registers itself', () => {
    const check = checkWith(policy());
    check.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(check);
  });

  it.each([
    ['the seeded values', {}, 'pass'],
    ['retention exactly 365 days', { offlineRetentionDays: 365 }, 'pass'],
    ['retention 364 days', { offlineRetentionDays: 364 }, 'warn'],
    ['the platform default of 30 days', { offlineRetentionDays: 30 }, 'warn'],
    ['stale after exactly 90 seconds', { staleHeartbeatSeconds: 90 }, 'pass'],
    ['stale after 91 seconds', { staleHeartbeatSeconds: 91 }, 'warn'],
    ['both out of range', { offlineRetentionDays: 30, staleHeartbeatSeconds: 120 }, 'warn'],
  ] as const)('%s -> %s', async (_name, over, status) => {
    const outcome = await checkWith(policy(over)).run();
    expect(outcome.status).toBe(status);
    expectContract(outcome);
  });

  it('reports each problem and the values', async () => {
    const outcome = await checkWith(policy({ offlineRetentionDays: 30, staleHeartbeatSeconds: 120 })).run();
    expect(outcome.detail).toContain('forgotten after 30 days');
    expect(outcome.detail).toContain('stale only after 120 seconds');
    expect(outcome.remedy).toContain('365');
    expect(outcome.remedy).toContain('90 seconds or less');
    expect(outcome.data).toEqual({ offlineRetentionDays: 30, staleHeartbeatSeconds: 120 });
  });

  it('names only the problem there is', async () => {
    const outcome = await checkWith(policy({ offlineRetentionDays: 30 })).run();
    expect(outcome.detail).not.toContain('stale only');
  });
});

describeDoctorCheckConformance('localai.settings.nodes', () => checkWith(policy()), {
  scenarios: [
    { name: 'fine', expectStatus: 'pass' },
    { name: 'pruned too soon', build: () => checkWith(policy({ offlineRetentionDays: 30 })), expectStatus: 'warn' },
  ],
});
