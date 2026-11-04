import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { fakePrisma } from '../../../test/localai/support/fake-tables';
import { expectContract } from '../../../test/localai/support/doctor-asserts';
import { CommandsStuckDoctorCheck } from './commands-stuck.check';

const registry = { register: jest.fn() };
const NOW = new Date('2026-11-10T12:00:00Z');
const minutesAgo = (minutes: number) => new Date(NOW.getTime() - minutes * 60_000);
const minutesAhead = (minutes: number) => new Date(NOW.getTime() + minutes * 60_000);
const checkWith = (commands: Array<Record<string, unknown>>) => new CommandsStuckDoctorCheck(registry as never, fakePrisma({ localaiCommand: commands }) as never);
const command = (status: string, expiresAt: Date, leaseExpiresAt: Date | null = null) => ({ status, expiresAt, leaseExpiresAt });

describe('localai.commands.stuck', () => {
  it('registers itself and links to the jobs page', () => {
    const check = checkWith([]);
    check.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(check);
    expect(check.settingsPath).toBe('/admin/settings/jobs');
  });

  it.each([
    ['nothing queued', [], 'pass'],
    ['queued, not yet expired', [command('queued', minutesAhead(10))], 'pass'],
    ['queued, expired 4 minutes ago (the sweeper is about to run)', [command('queued', minutesAgo(4))], 'pass'],
    ['queued, expired 6 minutes ago', [command('queued', minutesAgo(6))], 'warn'],
    ['running, lease ended 4 minutes ago', [command('running', minutesAhead(60), minutesAgo(4))], 'pass'],
    ['running, lease ended 6 minutes ago', [command('running', minutesAhead(60), minutesAgo(6))], 'warn'],
    ['running with a live lease', [command('running', minutesAhead(60), minutesAhead(2))], 'pass'],
    ['already settled commands never count', [command('expired', minutesAgo(60)), command('failed', minutesAgo(60), minutesAgo(60)), command('succeeded', minutesAgo(60))], 'pass'],
  ] as const)('%s -> %s', async (_name, commands, status) => {
    const outcome = await checkWith([...commands]).inspect(NOW);
    expect(outcome.status).toBe(status);
    expectContract(outcome);
  });

  it('counts both kinds with the LAI-CLD-030 code and fix', async () => {
    const outcome = await checkWith([
      command('queued', minutesAgo(9)),
      command('queued', minutesAgo(30)),
      command('running', minutesAhead(60), minutesAgo(8)),
    ]).inspect(NOW);
    expect(outcome.status).toBe('warn');
    expect(outcome.detail).toContain('LAI-CLD-030');
    expect(outcome.detail).toContain('2 queued past its expiry and 1 running past its lease');
    expect(outcome.remedy).toBe("Admin: check that the cloud's background jobs run (Admin → Jobs)");
    expect(outcome.data).toEqual({ queuedPastExpiry: 2, runningPastLease: 1, code: 'LAI-CLD-030' });
  });
});

describeDoctorCheckConformance('localai.commands.stuck', ({ prisma }) => new CommandsStuckDoctorCheck(registry as never, prisma.client), {
  scenarios: [
    { name: 'nothing stuck', prisma: { results: { 'localaiCommand.count': 0 } }, expectStatus: 'pass' },
    { name: 'stuck', prisma: { results: { 'localaiCommand.count': 3 } }, expectStatus: 'warn' },
  ],
});
