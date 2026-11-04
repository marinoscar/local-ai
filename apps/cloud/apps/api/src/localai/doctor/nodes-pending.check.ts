// `localai.nodes.pending` (LAI-CLD-031, warning): a node was added in the wizard and never connected.
// (docs/specs/cloud-app.md sections 5 and 7.) A `pending` node holds a live credential; after 7 days the admin is told
// which nodes to retire or finish.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { PrismaService } from '../../prisma/prisma.service';
import { CLOUD_CODES } from './codes';
import { buildOutcome, nameList } from './doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY, PENDING_NODE_AFTER_MS } from './doctor.constants';

@Injectable()
export class NodesPendingDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.nodes.pending';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'No node left waiting to connect';
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

  /** The check at a given moment (the unit tests fix it). */
  async inspect(now: Date): Promise<DoctorCheckOutcome> {
    const cutoff = new Date(now.getTime() - PENDING_NODE_AFTER_MS);
    const waiting = await this.prisma.localaiNode.findMany({
      where: { phase: 'pending', retiredAt: null, createdAt: { lt: cutoff } },
      select: { nodeId: true, createdAt: true },
      orderBy: { createdAt: 'asc' },
      take: 200,
    });
    if (waiting.length === 0) return buildOutcome({ status: 'pass', detail: 'No node has been waiting to connect for more than 7 days', data: { count: 0 } });

    const oldestDays = Math.floor((now.getTime() - waiting[0].createdAt.getTime()) / 86_400_000);
    const names = waiting.map((node) => node.nodeId);
    return buildOutcome({
      status: 'warn',
      code: CLOUD_CODES.nodeNeverConnected,
      found: `${names.length === 1 ? 'Retire or finish' : 'Retire or finish these'} ${nameList(names)}`,
      data: { count: names.length, oldestDays },
    });
  }
}
