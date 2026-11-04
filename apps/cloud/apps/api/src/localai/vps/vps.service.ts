// The cloud side of "Publish automatically" (docs/specs/cloud-app.md sections 4.8 and 4.10): what blocks a publish or an
// unpublish right now (in plain sentences), the enqueue, and the status the wizard polls. No VPS work happens here: it
// is all in the two jobs the stack-agent runs.
import { ConflictException, Inject, Injectable, Logger, NotFoundException } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { JobsService, NodeOffloadService } from '@marinoscar/platform-api/jobs';

import { PrismaService } from '../../prisma/prisma.service';
import { Route53KeyService } from './route53-key.service';
import { VPS_HOST_RE } from './vps-host';
import { readJournal } from './vps-journal';
import type { StoredJournal } from './vps-result.schema';
import { scrubVpsText } from './vps-scrub';
import { VpsSettingsService } from './vps-settings';
import {
  AUDIT_VPS_PUBLISH_REQUESTED,
  AUDIT_VPS_TARGET,
  AUDIT_VPS_UNPUBLISH_REQUESTED,
  JOB_VPS_PUBLISH,
  JOB_VPS_UNPUBLISH,
  VPS_AGENT_WORKER_NAME,
  VPS_JOB_SUBJECT,
  VPS_JOB_TYPES,
} from './vps.constants';

export type BlockerCode =
  | 'node_retired'
  | 'staging_suffix_invalid'
  | 'route53_not_configured'
  | 'tailnet_incomplete'
  | 'serve_pattern_none'
  | 'secrets_not_offered'
  | 'agent_offline'
  | 'server_worker_claims_jobs'
  | 'job_active';

export interface Blocker {
  code: BlockerCode;
  /** One plain sentence for the administrator (no secret, no address). */
  message: string;
}

export type VpsRunState = 'idle' | 'queued' | 'running' | 'succeeded' | 'failed';

export interface VpsStatusDto {
  nodeId: string;
  /** The host a publish would create (`ai-n3-staging.marin.cr`), or null while the suffix is invalid. */
  host: string | null;
  publish: { available: boolean; blockers: Blocker[] };
  unpublish: { available: boolean; blockers: Blocker[] };
  run: {
    state: VpsRunState;
    action: 'publish' | 'unpublish' | null;
    jobId: string | null;
    enqueuedAt: string | null;
    finishedAt: string | null;
    /** The journal of the last finished run, when it is this job's. */
    steps: Array<{ name: string; status: 'done' | 'skipped' | 'failed'; detail: string }>;
    certificateExpiresAt: string | null;
    /** The job's own error when it failed without a journal (for example the agent could not get its secret). */
    error: string | null;
  };
}

const ACTIVE = ['pending', 'running'] as const;

@Injectable()
export class VpsService {
  private readonly logger = new Logger(VpsService.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly jobs: JobsService,
    private readonly keys: Route53KeyService,
    private readonly settings: VpsSettingsService,
    private readonly offload: NodeOffloadService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  async status(nodeId: string): Promise<VpsStatusDto> {
    const node = await this.load(nodeId);
    const settings = await this.settings.read();
    const latest = await this.latestJob(nodeId);
    const blockers = await this.blockers(node, settings.stagingSuffixValid, latest);
    const host = settings.stagingSuffixValid ? `${nodeId}${settings.stagingSuffix}.marin.cr` : null;
    const journal: StoredJournal | null = latest ? readJournal(node.publishChecklist) : null;
    const mine = latest && journal && journal.jobId === latest.id ? journal : null;
    const state: VpsRunState = !latest ? 'idle' : latest.status === 'pending' ? 'queued' : latest.status === 'running' ? 'running' : latest.status === 'succeeded' ? 'succeeded' : 'failed';
    return {
      nodeId,
      host: host !== null && VPS_HOST_RE.test(host) ? host : null,
      publish: { available: blockers.publish.length === 0, blockers: blockers.publish },
      unpublish: { available: blockers.unpublish.length === 0, blockers: blockers.unpublish },
      run: {
        state,
        action: latest ? (latest.type === JOB_VPS_PUBLISH ? 'publish' : 'unpublish') : null,
        jobId: latest?.id ?? null,
        enqueuedAt: latest?.createdAt.toISOString() ?? null,
        finishedAt: latest?.finishedAt?.toISOString() ?? null,
        steps: mine?.steps ?? [],
        certificateExpiresAt: mine?.certificateExpiresAt ?? null,
        error: latest && latest.status === 'failed' && !mine && latest.lastError ? scrubVpsText(latest.lastError, 300) : null,
      },
    };
  }

  async requestPublish(nodeId: string, actorUserId: string | null): Promise<{ jobId: string }> {
    return this.request(nodeId, 'publish', actorUserId);
  }

  async requestUnpublish(nodeId: string, actorUserId: string | null): Promise<{ jobId: string }> {
    return this.request(nodeId, 'unpublish', actorUserId);
  }

  private async request(nodeId: string, action: 'publish' | 'unpublish', actorUserId: string | null): Promise<{ jobId: string }> {
    const status = await this.status(nodeId);
    const blockers = status[action].blockers;
    if (blockers.length > 0) {
      const first = blockers[0]!;
      throw new ConflictException({ message: first.message, details: { reason: first.code, blockers } });
    }
    // Params are exactly { nodeId }: the host, the key and the tailnet address come through the job's brokered secret.
    const job = await this.jobs.enqueue({
      type: action === 'publish' ? JOB_VPS_PUBLISH : JOB_VPS_UNPUBLISH,
      reason: 'upload',
      subjectType: VPS_JOB_SUBJECT,
      subjectId: nodeId,
      payload: { nodeId },
      orgId: null,
    });
    await this.record(action === 'publish' ? AUDIT_VPS_PUBLISH_REQUESTED : AUDIT_VPS_UNPUBLISH_REQUESTED, nodeId, actorUserId, { jobId: job.id, host: status.host });
    return { jobId: job.id };
  }

  private async load(nodeId: string) {
    const node = await this.prisma.localaiNode.findUnique({
      where: { nodeId },
      select: { nodeId: true, phase: true, servePattern: true, tailnetIp: true, tailnetMachine: true, publishChecklist: true },
    });
    if (!node) throw new NotFoundException({ message: 'No Local AI node has this id', details: { reason: 'unknown_localai_node' } });
    return node;
  }

  private latestJob(nodeId: string) {
    return this.prisma.job.findFirst({
      where: { type: { in: [...VPS_JOB_TYPES] }, subjectType: VPS_JOB_SUBJECT, subjectId: nodeId },
      orderBy: { createdAt: 'desc' },
      select: { id: true, type: true, status: true, createdAt: true, finishedAt: true, lastError: true },
    });
  }

  private async blockers(
    node: Awaited<ReturnType<VpsService['load']>>,
    suffixValid: boolean,
    latest: { status: string } | null,
  ): Promise<{ publish: Blocker[]; unpublish: Blocker[] }> {
    const common: Blocker[] = [];
    const publishOnly: Blocker[] = [];
    if (node.phase === 'retired') common.push({ code: 'node_retired', message: 'This node is retired.' });
    if (!suffixValid) common.push({ code: 'staging_suffix_invalid', message: 'The setting vps.stagingSuffix is not valid (empty, or a dash and up to 16 lower-case letters or digits).' });
    if (latest && (ACTIVE as readonly string[]).includes(latest.status)) common.push({ code: 'job_active', message: 'A publish or unpublish is already queued or running for this node.' });

    const keyStatus = await this.keys.status().catch(() => null);
    if (!keyStatus?.configured) common.push({ code: 'route53_not_configured', message: 'The Route53 key is not stored. Add it under Admin → Settings → VPS publishing.' });

    if (process.env.JOBS_WORKER_MODE?.trim().toLowerCase() !== 'system' && process.env.JOBS_WORKER_MODE?.trim().toLowerCase() !== 'off') {
      common.push({ code: 'server_worker_claims_jobs', message: 'The API server still runs every job type itself. Set JOBS_WORKER_MODE=system and restart it (runbook 10, part 10).' });
    }
    // With no key stored the platform withholds the types too (the broker is not usable); that is already reported above.
    const offered = keyStatus?.configured ? await this.offload.offeredTypes().catch(() => [] as string[]) : [JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH];
    if (!offered.includes(JOB_VPS_PUBLISH) || !offered.includes(JOB_VPS_UNPUBLISH)) {
      common.push({ code: 'secrets_not_offered', message: 'Node job secrets are switched off. Turn on "nodes.jobSecretBrokerEnabled" in the system settings (runbook 10, part 10).' });
    }
    const agent = await this.prisma.workerNode.findFirst({ where: { name: VPS_AGENT_WORKER_NAME, status: 'online' }, select: { eligibleTypes: true } });
    if (!agent || !agent.eligibleTypes.includes(JOB_VPS_PUBLISH) || !agent.eligibleTypes.includes(JOB_VPS_UNPUBLISH)) {
      common.push({ code: 'agent_offline', message: 'The VPS stack-agent is not connected. Start the localai-vps-agent service on the VPS (runbook 10, part 10).' });
    }

    if (node.servePattern !== 'A' && node.servePattern !== 'B') publishOnly.push({ code: 'serve_pattern_none', message: 'This node has no serve pattern (A or B).' });
    const needsFullName = node.servePattern === 'B';
    if (!node.tailnetIp || !node.tailnetMachine || (needsFullName && !node.tailnetMachine.includes('.'))) {
      publishOnly.push({
        code: 'tailnet_incomplete',
        message: needsFullName ? 'The node needs its tailnet IP and its full tailnet name (Pattern B). Add them in the node’s Settings.' : 'The node needs its tailnet IP and machine name. Add them in the node’s Settings.',
      });
    }
    return { publish: [...common, ...publishOnly], unpublish: common };
  }

  /** After the enqueue. A failed audit write is logged by error name and never undoes the request. */
  private async record(action: string, nodeId: string, actorUserId: string | null, meta: Record<string, string | number | boolean | null>): Promise<void> {
    try {
      await this.audit.record({ action, actorUserId, targetType: AUDIT_VPS_TARGET, targetId: nodeId, meta });
    } catch (error) {
      this.logger.error(`audit ${action} failed for node_id=${nodeId}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }
}
