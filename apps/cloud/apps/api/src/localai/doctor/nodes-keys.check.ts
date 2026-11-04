// `localai.nodes.keys` (LAI-CLD-026, warning): an enrolled node whose key the cloud does not hold.
// (docs/specs/cloud-app.md sections 3.8 and 7.) The gateway cannot use a node without its llama key. A node reports the
// key right after enrolling, so one that has been enrolled for 10 minutes with `key_reported_at` still null needs help.
//
// "Enrolled for 10 minutes" starts at the `localai.node.enrolled` audit row the binding guard writes when `pending`
// becomes `enrolled` (the node row has no enrollment timestamp); a node without such a row (history purged) is timed
// from the moment its record was created, which is never later.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { PrismaService } from '../../prisma/prisma.service';
import { AUDIT_NODE_ENROLLED } from '../node-link/binding.guard';
import { CLOUD_CODES } from './codes';
import { buildOutcome, nameList } from './doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY, NODE_KEY_GRACE_MS } from './doctor.constants';

@Injectable()
export class NodesKeysDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.nodes.keys';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'The cloud holds every enrolled node key';
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
    const keyless = await this.prisma.localaiNode.findMany({
      where: { phase: 'enrolled', retiredAt: null, keyReportedAt: null },
      select: { nodeId: true, createdAt: true },
      orderBy: { nodeId: 'asc' },
      take: 200,
    });
    if (keyless.length === 0) return buildOutcome({ status: 'pass', detail: 'Every enrolled node has a stored key', data: { count: 0 } });

    const enrollments = await this.prisma.auditEvent.findMany({
      where: { action: AUDIT_NODE_ENROLLED, targetType: 'localai_node', targetId: { in: keyless.map((node) => node.nodeId) } },
      select: { targetId: true, createdAt: true },
    });
    const enrolledAt = new Map<string, Date>();
    for (const event of enrollments) {
      const seen = enrolledAt.get(event.targetId);
      if (!seen || event.createdAt > seen) enrolledAt.set(event.targetId, event.createdAt);
    }

    const cutoff = now.getTime() - NODE_KEY_GRACE_MS;
    const overdue = keyless.filter((node) => (enrolledAt.get(node.nodeId) ?? node.createdAt).getTime() <= cutoff).map((node) => node.nodeId);
    if (overdue.length === 0) {
      return buildOutcome({ status: 'pass', detail: 'Every node enrolled for over 10 minutes has a stored key', data: { count: 0, waiting: keyless.length } });
    }
    return buildOutcome({
      status: 'warn',
      code: CLOUD_CODES.nodeKeyMissing,
      found: `no key stored for ${nameList(overdue)}`,
      data: { count: overdue.length },
    });
  }
}
