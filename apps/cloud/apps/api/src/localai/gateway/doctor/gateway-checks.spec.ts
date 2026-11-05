// The four gateway Doctor checks (docs/specs/gateway.md section 7): a pass and a fail case each, the contract (one line,
// a remedy on warn, no secret shape), registration, the read-only rule, and the platform's own conformance harness.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { expectContract, expectNoSecretShapes, leakBait } from '../../../../test/localai/support/doctor-asserts';
import { GatewayEnabledDoctorCheck } from './gateway-enabled.check';
import { GatewayKeysDoctorCheck } from './gateway-keys.check';
import { GatewayRoutableDoctorCheck } from './gateway-routable.check';
import { GatewayUsageDoctorCheck, USAGE_DROP_WINDOW_MS } from './gateway-usage.check';
import { GATEWAY_DOCTOR_CHECK_IDS } from './gateway-doctor.module';

const registry = () => ({ register: jest.fn() });

const enabledCheck = (over: { enabled?: boolean; maintenance?: boolean | Error } = {}) =>
  new GatewayEnabledDoctorCheck(
    registry() as never,
    { enabled: () => over.enabled ?? true },
    {
      isEnabled: async () => {
        if (over.maintenance instanceof Error) throw over.maintenance;
        return over.maintenance ?? false;
      },
    },
  );
const routableCheck = (count: number) => new GatewayRoutableDoctorCheck(registry() as never, { routableNodeCount: () => count });
const keysCheck = (nodes: string[]) => new GatewayKeysDoctorCheck(registry() as never, { keyInvalidNodes: () => nodes });
const usageCheck = (dropped: number) => new GatewayUsageDoctorCheck(registry() as never, { droppedSince: jest.fn(() => dropped) });

describe('localai.gateway.enabled (LAI-GW-051)', () => {
  it('passes when the gateway is on and the site is not in maintenance', async () => {
    const outcome = await enabledCheck().run();
    expect(outcome.status).toBe('pass');
    expectContract(outcome);
  });

  it('warns when LOCALAI_GATEWAY_ENABLED is false', async () => {
    const outcome = await enabledCheck({ enabled: false }).run();
    expect(outcome.status).toBe('warn');
    expect(outcome.detail).toContain('LAI-GW-051');
    expect(outcome.remedy).toContain('LOCALAI_GATEWAY_ENABLED');
    expect(outcome.data).toMatchObject({ code: 'LAI-GW-051', enabled: false });
    expectContract(outcome);
  });

  it('warns in maintenance mode', async () => {
    const outcome = await enabledCheck({ maintenance: true }).run();
    expect(outcome.status).toBe('warn');
    expect(outcome.detail).toContain('maintenance mode is on');
    expectContract(outcome);
  });

  it('skips (not evaluated) when the maintenance state cannot be read, naming the error and not its message', async () => {
    const bait = leakBait();
    const outcome = await enabledCheck({ maintenance: new TypeError(`db at ${bait.ip}`) }).run();
    expect(outcome.status).toBe('skip');
    expect(outcome.error).toBe('TypeError');
    expectNoSecretShapes(outcome);
  });
});

describe('localai.gateway.routable (LAI-GW-021)', () => {
  it.each([
    [0, 'warn'],
    [1, 'pass'],
    [3, 'pass'],
  ] as const)('%d routable node(s) -> %s', async (count, status) => {
    const outcome = await routableCheck(count).run();
    expect(outcome.status).toBe(status);
    expectContract(outcome);
    if (status === 'warn') expect(outcome.data).toMatchObject({ code: 'LAI-GW-021', count: 0 });
  });
});

describe('localai.gateway.keys (LAI-GW-043)', () => {
  it('passes when no node refused its key', async () => {
    const outcome = await keysCheck([]).run();
    expect(outcome.status).toBe('pass');
    expectContract(outcome);
  });

  it('warns naming the nodes (sorted), with the fix of the code', async () => {
    const outcome = await keysCheck(['ai-n3', 'ai-n1']).run();
    expect(outcome.status).toBe('warn');
    expect(outcome.detail).toContain('ai-n1, ai-n3 refused the stored key');
    expect(outcome.remedy).toContain('sudo aictl cloud resend-key');
    expect(outcome.data).toEqual({ count: 2, code: 'LAI-GW-043' });
    expectContract(outcome);
  });

  it('scrubs a node id made of bait', async () => {
    const bait = leakBait();
    expectNoSecretShapes(await keysCheck([`${bait.token}-${bait.ip}`]).run());
  });
});

describe('localai.gateway.usage (LAI-GW-060)', () => {
  it('passes when nothing was dropped in the last hour', async () => {
    const check = usageCheck(0);
    const outcome = await check.run();
    expect(outcome.status).toBe('pass');
    expectContract(outcome);
  });

  it('warns when rows were dropped in the last hour, with the count', async () => {
    const dropped = jest.fn(() => 7);
    const outcome = await new GatewayUsageDoctorCheck(registry() as never, { droppedSince: dropped }).run();
    expect(dropped).toHaveBeenCalledWith(USAGE_DROP_WINDOW_MS);
    expect(outcome.status).toBe('warn');
    expect(outcome.detail).toContain('7 dropped in the last hour');
    expect(outcome.remedy).toContain('check the database');
    expect(outcome.data).toEqual({ dropped: 7, code: 'LAI-GW-060' });
    expectContract(outcome);
  });
});

describe('the four gateway checks as a set', () => {
  it('register themselves with the platform registry', () => {
    for (const check of [enabledCheck(), routableCheck(1), keysCheck([]), usageCheck(0)]) {
      const reg = (check as unknown as { registry: { register: jest.Mock } }).registry;
      check.onModuleInit();
      expect(reg.register).toHaveBeenCalledWith(check);
    }
  });

  it('have the ids of the spec, unique, in the localai category', () => {
    const checks = [enabledCheck(), routableCheck(1), keysCheck([]), usageCheck(0)];
    expect(checks.map((check) => check.id)).toEqual([...GATEWAY_DOCTOR_CHECK_IDS]);
    expect(new Set(checks.map((check) => check.id)).size).toBe(4);
    for (const check of checks) expect(check.category).toBe('localai');
  });

  it('have sources that write nothing (the Doctor is read-only)', () => {
    const forbidden = /\.(create|createMany|update|updateMany|upsert|delete|deleteMany)\(|\$executeRaw|\$queryRaw|\$transaction|\.enqueue\(|\.record\(|\.publish\(|\.notify\(|\.markKeyInvalid\(|\.markSuspect\(/;
    for (const name of ['gateway-enabled.check.ts', 'gateway-routable.check.ts', 'gateway-keys.check.ts', 'gateway-usage.check.ts']) {
      const stripped = readFileSync(join(__dirname, name), 'utf8')
        .split('\n')
        .filter((line) => !line.trim().startsWith('//'))
        .join('\n');
      expect({ file: name, write: forbidden.exec(stripped)?.[0] ?? null }).toEqual({ file: name, write: null });
    }
  });
});

describeDoctorCheckConformance('localai.gateway.enabled', () => enabledCheck(), {
  scenarios: [
    { name: 'on', expectStatus: 'pass' },
    { name: 'off', build: () => enabledCheck({ enabled: false }), expectStatus: 'warn' },
  ],
});
describeDoctorCheckConformance('localai.gateway.routable', () => routableCheck(2), {
  scenarios: [
    { name: 'two nodes', expectStatus: 'pass' },
    { name: 'none', build: () => routableCheck(0), expectStatus: 'warn' },
  ],
});
describeDoctorCheckConformance('localai.gateway.keys', () => keysCheck([]), {
  scenarios: [
    { name: 'no refused key', expectStatus: 'pass' },
    { name: 'a refused key', build: () => keysCheck(['ai-n1']), expectStatus: 'warn' },
  ],
});
describeDoctorCheckConformance('localai.gateway.usage', () => usageCheck(0), {
  scenarios: [
    { name: 'nothing dropped', expectStatus: 'pass' },
    { name: 'rows dropped', build: () => usageCheck(3), expectStatus: 'warn' },
  ],
});
