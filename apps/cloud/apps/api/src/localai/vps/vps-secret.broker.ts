// The per-job secret broker of the two VPS job types (docs/specs/cloud-app.md section 4.10; EnterpriseAppBase
// `JobSecretBroker`). The stack-agent asks `POST /api/nodes/:id/jobs/:jobId/secret` for the Route53 key and the node's
// tailnet address and name; they are never in the job's payload and are returned once, held in the agent's memory
// only and never persisted by it.
//
// What is stored: the platform records the HANDLE only (`job_node_secrets.handle`). Here the handle is `vps:<jobId>`:
// there is nothing to revoke because the broker does not mint a backend grant (the IAM key is the standing,
// least-privilege key an administrator stored); `revoke` is therefore a deliberate no-op that tolerates any handle.
import { Injectable } from '@nestjs/common';
import type { IssuedJobSecret, Job, JobSecretBroker, JobSecretUsability } from '@marinoscar/platform-api/jobs';

import { PrismaService } from '../../prisma/prisma.service';
import { Route53KeyService } from './route53-key.service';
import { isAllowedVpsHost, vpsHostFor } from './vps-host';
import { JOB_VPS_PUBLISH, VPS_SECRET_KIND } from './vps.constants';
import { VpsSettingsService } from './vps-settings';

/** A refusal with a fixed message (it reaches the node's log): no value, no address. */
export class VpsSecretRefusedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'VpsSecretRefusedError';
  }
}

@Injectable()
export class VpsSecretBroker implements JobSecretBroker {
  readonly kind = VPS_SECRET_KIND;

  constructor(
    private readonly prisma: PrismaService,
    private readonly keys: Route53KeyService,
    private readonly settings: VpsSettingsService,
  ) {}

  /** Can it issue now? Only whether a complete key is stored. Creates nothing. */
  async usable(): Promise<JobSecretUsability> {
    try {
      const status = await this.keys.status();
      if (status.configured) return { ok: true };
    } catch {
      // fall through to the refusal below
    }
    return {
      ok: false,
      reason: 'No Route53 key is stored for the VPS stack-agent.',
      remedy: 'Open Admin → Settings → VPS publishing and save the Route53 key.',
    };
  }

  async issue(job: Job, until: Date): Promise<IssuedJobSecret> {
    const nodeId = nodeIdOf(job);
    const settings = await this.settings.read();
    if (!settings.stagingSuffixValid) throw new VpsSecretRefusedError('vps.stagingSuffix is not valid.');
    const host = vpsHostFor(nodeId, settings.stagingSuffix);
    if (!isAllowedVpsHost(host, nodeId, settings.stagingSuffix)) throw new VpsSecretRefusedError('The host is not allowed.');

    const key = await this.keys.read();
    if (key === null) throw new VpsSecretRefusedError('No Route53 key is stored.');

    const material: Record<string, unknown> = {
      route53AccessKeyId: key.accessKeyId,
      route53SecretAccessKey: key.secretAccessKey,
      hostedZoneId: key.hostedZoneId,
      host,
      stagingSuffix: settings.stagingSuffix,
      vpsPublicHostname: settings.vpsPublicHostname,
    };

    if (job.type === JOB_VPS_PUBLISH) {
      const node = await this.prisma.localaiNode.findUnique({
        where: { nodeId },
        select: { phase: true, servePattern: true, tailnetIp: true, tailnetMachine: true },
      });
      if (!node || node.phase === 'retired') throw new VpsSecretRefusedError('The node does not exist or is retired.');
      if (node.servePattern !== 'A' && node.servePattern !== 'B') throw new VpsSecretRefusedError('The node has no serve pattern (A or B).');
      if (!node.tailnetIp || !node.tailnetMachine) throw new VpsSecretRefusedError('The node has no tailnet address or machine name stored.');
      material.servePattern = node.servePattern;
      material.tailnetIp = node.tailnetIp;
      material.tailnetMachine = node.tailnetMachine;
    }

    // The platform asked for a grant lasting until the lease ends; this credential is the standing key, so the lease is
    // the only bound that applies to how long the node may hold it.
    return { handle: `vps:${job.id}`, expiresAt: until, material };
  }

  async revoke(): Promise<void> {
    // Nothing was minted, so there is nothing to undo; "already gone" is success.
  }
}

/** The node id a VPS job is about: its payload `{ nodeId }` (the subject carries the same value). */
export function nodeIdOf(job: Pick<Job, 'payload' | 'subjectId'>): string {
  const payload = job.payload;
  const fromPayload = typeof payload === 'object' && payload !== null && !Array.isArray(payload) ? (payload as Record<string, unknown>).nodeId : undefined;
  const nodeId = typeof fromPayload === 'string' ? fromPayload : job.subjectId;
  if (typeof nodeId !== 'string' || !/^ai-n[1-9][0-9]{0,3}$/.test(nodeId)) throw new VpsSecretRefusedError('The job names no lab node.');
  return nodeId;
}
