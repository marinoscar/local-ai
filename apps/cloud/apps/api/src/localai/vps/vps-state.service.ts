// Persists a finished publish/unpublish run (docs/specs/cloud-app.md section 4.10): the journal under the reserved key
// `vps` of `publish_checklist`, and the wizard's checklist steps 2 to 5 moved by it. One row-locked write, so a
// checklist action made at the same time is not lost; the audit row is written after it commits.
//
// What reaches the database is the node's result AFTER the strict schema and a second scrub: a host that is not this
// node's under the configured suffix is refused outright.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import type { Job } from '@marinoscar/platform-api/jobs';
import type { Prisma } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { parseStoredChecklist } from '../wizard/checklist.state';
import { nodeIdOf } from './vps-secret.broker';
import { isAllowedVpsHost } from './vps-host';
import { buildJournal, checklistUpdates, failedStep } from './vps-journal';
import type { PublishResult, UnpublishResult } from './vps-result.schema';
import { VpsSettingsService } from './vps-settings';
import { AUDIT_VPS_PUBLISHED, AUDIT_VPS_PUBLISH_FAILED, AUDIT_VPS_TARGET, AUDIT_VPS_UNPUBLISHED, VPS_JOURNAL_KEY } from './vps.constants';

@Injectable()
export class VpsStateService {
  private readonly logger = new Logger(VpsStateService.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly settings: VpsSettingsService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  /**
   * Stores the run. Throws (so the job fails and the wizard shows it) after storing when the journal holds a failed
   * step; the message names the step only.
   */
  async persist(job: Job, action: 'publish' | 'unpublish', result: PublishResult | UnpublishResult, now: Date = new Date()): Promise<void> {
    const nodeId = nodeIdOf(job);
    const settings = await this.settings.read();
    if (!settings.stagingSuffixValid || !isAllowedVpsHost(result.host, nodeId, settings.stagingSuffix)) {
      throw new Error('The stack-agent reported a host that is not the host of this node.');
    }
    const journal = buildJournal({
      jobId: job.id,
      action,
      host: result.host,
      steps: result.steps,
      certificateExpiresAt: 'certificateExpiresAt' in result ? result.certificateExpiresAt : null,
      now,
    });

    const found = await this.prisma.localaiNode.findUnique({ where: { nodeId }, select: { id: true } });
    if (!found) throw new Error('The node no longer exists.');
    await this.prisma.$transaction(async (tx) => {
      await tx.$queryRaw`SELECT id FROM localai_nodes WHERE id = ${found.id}::uuid FOR UPDATE`;
      const current = await tx.localaiNode.findUniqueOrThrow({ where: { id: found.id }, select: { publishChecklist: true, phase: true } });
      // A retired node keeps its journal but its checklist is not touched (every checklist route refuses it).
      const updates = current.phase === 'retired' ? {} : checklistUpdates(journal, now);
      const merged = { ...parseStoredChecklist(current.publishChecklist), ...updates, [VPS_JOURNAL_KEY]: journal };
      await tx.localaiNode.update({ where: { id: found.id }, data: { publishChecklist: merged as unknown as Prisma.InputJsonObject } });
    });

    const failed = failedStep(journal);
    await this.record(failed ? AUDIT_VPS_PUBLISH_FAILED : action === 'publish' ? AUDIT_VPS_PUBLISHED : AUDIT_VPS_UNPUBLISHED, nodeId, {
      action,
      host: journal.host,
      jobId: job.id,
      steps: journal.steps.map((step) => `${step.name}:${step.status}`).join(','),
      ...(failed ? { failedStep: failed.name } : {}),
    });
    if (failed) throw new Error(`The VPS ${action} did not finish: step ${failed.name} failed.`);
  }

  /** After the write committed. A failed audit write is logged by error name and never loses the journal. */
  private async record(action: string, nodeId: string, meta: Record<string, string | number | boolean | null>): Promise<void> {
    try {
      await this.audit.record({ action, actorUserId: null, targetType: AUDIT_VPS_TARGET, targetId: nodeId, meta });
    } catch (error) {
      this.logger.error(`audit ${action} failed for node_id=${nodeId}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }
}
