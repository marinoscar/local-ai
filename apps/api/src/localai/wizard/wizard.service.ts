// The Add-node wizard's create step (docs/specs/cloud-app.md section 4.8): a `pending` node, one `nod_` credential
// minted for it and bound to it, and the install block. The raw token leaves this class in exactly one place, the
// return value of `create`.
//
// SECRET HANDLING (spec section 6).
//  - The token is held in a local variable between `createCredential` and `return`. It is not logged, not in the audit
//    rows (they carry the credential's id and its 8-character prefix), not in an error message, not stored.
//  - If anything after the mint fails, the credential is revoked and no node row remains, so no live token is left
//    that nobody was shown.
//  - An audit or log failure after the commit never loses the token: the operator was just handed the only copy.
import { ConflictException, Inject, Injectable, Logger } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { NodeCredentialService, type NodeCredentialCreated } from '@marinoscar/platform-api/nodes';
import { Prisma } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { buildInstall, type InstallInfo } from './install-info';
import { derivePublicHost, nextFreeNodeId } from './node-id';
import { RELEASE_CHECKSUMS, type ReleaseChecksums } from './release-checksums.port';
import { WizardSettingsService } from './wizard-settings';
import type { CreateNodeBody } from './wizard.schemas';

/** Audit actions (permanent strings, spec section 3.5). */
export const AUDIT_NODE_CREATED = 'localai.node.created';
export const AUDIT_NODE_CREDENTIAL_MINTED = 'localai.node.credential_minted';

export interface CreateNodeResponse {
  node: {
    nodeId: string;
    displayName: string;
    publicHost: string;
    servePattern: 'A' | 'B';
    phase: 'pending';
  };
  /** SHOWN ONCE. `token` is the only copy; `prefix` is what lists show afterwards. */
  credential: { token: string; prefix: string };
  install: InstallInfo;
}

/** The `details.reason` of the refusals the wizard UI matches on. Permanent strings. */
export const NODE_ID_USED = 'node_id_used';

@Injectable()
export class WizardService {
  private readonly logger = new Logger(WizardService.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly credentials: NodeCredentialService,
    private readonly settings: WizardSettingsService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(RELEASE_CHECKSUMS) private readonly checksums: ReleaseChecksums,
  ) {}

  /** `ai-n<max+1>` over every row, retired ones included. Null when the numbers are used up. */
  async suggestNodeId(): Promise<string | null> {
    const rows = await this.prisma.localaiNode.findMany({ select: { nodeId: true } });
    return nextFreeNodeId(rows.map((row) => row.nodeId));
  }

  async create(input: CreateNodeBody, actorUserId: string): Promise<CreateNodeResponse> {
    const { nodeId } = input;
    // A fast, friendly refusal; the unique index on `node_id` is the real guard (two creates can race past this).
    if (await this.prisma.localaiNode.findUnique({ where: { nodeId }, select: { id: true } })) throw nodeIdUsed();

    const publicHost = input.publicHost ?? derivePublicHost(nodeId);
    const displayName = input.displayName ?? nodeId;
    // Filled inside the transaction; read after it (and in the failure path) to revoke a credential minted before an error.
    const held: { credential: NodeCredentialCreated | null } = { credential: null };
    try {
      await this.prisma.$transaction(async (tx) => {
        const row = await tx.localaiNode.create({
          data: {
            nodeId,
            displayName,
            publicHost,
            servePattern: input.servePattern,
            tailnetMachine: input.tailnetMachine ?? null,
            tailnetIp: input.tailnetIp ?? null,
            profile: input.profile ?? null,
            phase: 'pending',
            createdById: actorUserId,
          },
          select: { id: true },
        });
        // `expiresInDays` is left out on purpose: absent means "never expires" (revocable). `null` would compute an
        // expiry of "now" in the platform's service and mint a credential that is dead on arrival.
        held.credential = await this.credentials.createCredential(actorUserId, { name: `localai-${nodeId}` });
        await tx.localaiNode.update({ where: { id: row.id }, data: { nodeCredentialId: held.credential.id } });
      });
    } catch (error) {
      // The node row rolled back with the transaction; a credential minted before the failure must not stay live.
      if (held.credential) await this.revokeQuietly(actorUserId, held.credential.id, nodeId);
      // A unique violation on the node id is a lost race with another create of the same id; any other error is ours.
      if (error instanceof Prisma.PrismaClientKnownRequestError && error.code === 'P2002' && (await this.prisma.localaiNode.findUnique({ where: { nodeId }, select: { id: true } }))) {
        throw nodeIdUsed();
      }
      throw error;
    }

    const credential = held.credential;
    if (!credential) throw new Error('the credential was not minted'); // unreachable: the transaction would have thrown

    await this.record(AUDIT_NODE_CREATED, actorUserId, nodeId, { servePattern: input.servePattern, publicHost, tailnetIp: input.tailnetIp ? '<set>' : null });
    await this.record(AUDIT_NODE_CREDENTIAL_MINTED, actorUserId, nodeId, { credentialId: credential.id, credentialPrefix: credential.tokenPrefix });

    return {
      node: { nodeId, displayName, publicHost, servePattern: input.servePattern, phase: 'pending' },
      credential: { token: credential.token, prefix: credential.tokenPrefix },
      install: await this.installFor(nodeId),
    };
  }

  /**
   * The install block for a node: pinned to the latest configured agent release. It never throws: this runs after the
   * node and its credential are committed, and a failure here must not lose the only copy of the token.
   */
  private async installFor(nodeId: string): Promise<InstallInfo> {
    try {
      const settings = await this.settings.read();
      const sha256 = settings.agentLatestVersion ? await this.checksums.scriptSha256(settings.githubRepo, settings.agentLatestVersion) : null;
      return buildInstall({ repo: settings.githubRepo, version: settings.agentLatestVersion, nodeId, cloudUrl: `https://${settings.vpsPublicHostname}`, sha256 });
    } catch (error) {
      this.logger.error(`could not build the install block for node_id=${nodeId}: ${errorName(error)}`);
      return buildInstall({ repo: '', version: null, nodeId, cloudUrl: '', sha256: null });
    }
  }

  private async revokeQuietly(userId: string, credentialId: string, nodeId: string): Promise<void> {
    try {
      await this.credentials.revokeCredential(userId, credentialId);
    } catch (error) {
      this.logger.error(`could not revoke the credential of the failed node_id=${nodeId}: ${errorName(error)}`);
    }
  }

  /** After the commit, outside any transaction. A failed audit is logged by name only and never loses the token. */
  private async record(action: string, actorUserId: string, nodeId: string, meta: Record<string, string | number | boolean | null>): Promise<void> {
    try {
      await this.audit.record({ action, actorUserId, targetType: 'localai_node', targetId: nodeId, meta });
    } catch (error) {
      this.logger.error(`audit ${action} failed for node_id=${nodeId}: ${errorName(error)}`);
    }
  }
}

function nodeIdUsed(): ConflictException {
  return new ConflictException({
    message: 'This node id has been used before. Ids are never reused, even for a retired node. Pick the next free one.',
    details: { reason: NODE_ID_USED },
  });
}

function errorName(error: unknown): string {
  return error instanceof Error ? error.name : 'unknown error';
}
