// `localai.commands.stuck` (LAI-CLD-030, warning): commands the sweeper should have settled by now.
// (docs/specs/cloud-app.md sections 4.4, 5 and 7.) The sweeper runs every 30 seconds; a `queued` command past its
// `expiresAt`, or a `running` one past its lease, that is still unsettled 5 minutes later means the background jobs
// are not running.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { PrismaService } from '../../prisma/prisma.service';
import { CLOUD_CODES } from './codes';
import { buildOutcome } from './doctor-outcome';
import { COMMAND_SWEEP_GRACE_MS, LOCALAI_DOCTOR_CATEGORY } from './doctor.constants';

@Injectable()
export class CommandsStuckDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.commands.stuck';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'Commands are swept on time';
  readonly settingsPath = '/admin/settings/jobs';
  readonly timeoutMs = 3_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly prisma: PrismaService,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  run(): Promise<DoctorCheckOutcome> {
    return this.inspect(new Date());
  }

  async inspect(now: Date): Promise<DoctorCheckOutcome> {
    const cutoff = new Date(now.getTime() - COMMAND_SWEEP_GRACE_MS);
    const [queuedPastExpiry, runningPastLease] = await Promise.all([
      this.prisma.localaiCommand.count({ where: { status: 'queued', expiresAt: { lt: cutoff } } }),
      this.prisma.localaiCommand.count({ where: { status: 'running', leaseExpiresAt: { lt: cutoff } } }),
    ]);
    const data = { queuedPastExpiry, runningPastLease };
    if (queuedPastExpiry === 0 && runningPastLease === 0) {
      return buildOutcome({ status: 'pass', detail: 'No command is waiting past its expiry or lease', data });
    }
    const parts = [
      queuedPastExpiry > 0 ? `${queuedPastExpiry} queued past its expiry` : null,
      runningPastLease > 0 ? `${runningPastLease} running past its lease` : null,
    ].filter(Boolean);
    return buildOutcome({ status: 'warn', code: CLOUD_CODES.commandsWaiting, found: `${parts.join(' and ')}, not swept for over 5 minutes`, data });
  }
}
