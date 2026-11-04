// The write side of one node for lab:admin (docs/specs/cloud-app.md sections 3.9 and 3.11): edit its settings, replace
// its credential, retire it. Every change is audited through the platform's `AUDIT_SINK` AFTER it committed, outside any
// transaction.
//
// SECRETS. The new `nod_` token is returned once by `replaceCredential` and nowhere else: it is not stored (only EAB's
// hash is), not audited (the row carries the credential id and its prefix) and not logged. The tailnet address is
// audited as the word `<set>` or `<cleared>`, never as a value.
import { BadRequestException, ConflictException, Inject, Injectable, Logger, NotFoundException } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';
import type { LocalaiNode, Prisma } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { NodeKeyService } from '../keys/node-key.service';
import type { PatchNodeBody } from './dto/patch-node';
import { AUDIT_NODE_TARGET } from './lab-nodes.service';

/** Audit actions (permanent strings, spec section 3.5). */
export const AUDIT_NODE_UPDATED = 'localai.node.updated';
export const AUDIT_NODE_RETIRED = 'localai.node.retired';
export const AUDIT_NODE_CREDENTIAL_MINTED = 'localai.node.credential_minted';
export const AUDIT_NODE_CREDENTIAL_REVOKED = 'localai.node.credential_revoked';

/** What `POST …/credential` answers: the token is shown once and never again. */
export interface ReplacedCredential {
  nodeId: string;
  token: string;
  tokenPrefix: string;
  credentialId: string;
  createdAt: string;
  /** The shell steps the operator runs on the computer (no secrets in them). */
  reenroll: string[];
}

export interface RetiredNode {
  nodeId: string;
  phase: 'retired';
  cancelledCommands: number;
}

const REENROLL_STEPS = ['On the node, run: sudo aictl cloud enroll', 'Paste the new credential when it asks, then press Enter'];

@Injectable()
export class NodesAdminService {
  private readonly logger = new Logger(NodesAdminService.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly credentials: NodeCredentialService,
    private readonly keys: NodeKeyService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  /** Applies the changed fields of `body`; answers the nodeId and what changed (names, tailnet IP as a flag). */
  async patch(nodeId: string, body: PatchNodeBody, actorUserId: string | null): Promise<{ nodeId: string; changed: string[] }> {
    const node = await this.requireLive(nodeId);
    const data: Prisma.LocalaiNodeUpdateInput = {};
    const changes: Record<string, string | number | boolean | null> = {};
    const set = <K extends keyof PatchNodeBody & keyof LocalaiNode>(key: K, value: PatchNodeBody[K]) => {
      if (value === undefined || value === node[key]) return;
      (data as Record<string, unknown>)[key] = value;
      // The tailnet address is audited as a word, never as the value.
      changes[key] = key === 'tailnetIp' ? (value === null ? '<cleared>' : '<set>') : (value as string | number | boolean | null);
    };
    set('displayName', body.displayName);
    set('publicHost', body.publicHost);
    set('servePattern', body.servePattern);
    set('tailnetMachine', body.tailnetMachine);
    set('tailnetIp', body.tailnetIp);
    set('gatewayEnabled', body.gatewayEnabled);
    set('gatewayPriority', body.gatewayPriority);
    set('catalogPin', body.catalogPin);

    const changed = Object.keys(changes);
    if (changed.length === 0) return { nodeId, changed };
    try {
      await this.prisma.localaiNode.update({ where: { id: node.id }, data });
    } catch (error) {
      if ((error as { code?: string })?.code === 'P2002') {
        throw new ConflictException({ message: 'Another node already uses this value', details: { reason: 'duplicate' } });
      }
      throw error;
    }
    await this.record(AUDIT_NODE_UPDATED, actorUserId, nodeId, changes);
    return { nodeId, changed };
  }

  /**
   * Replaces the node's `nod_` credential: mints the new one and binds it, then revokes the old one. The old credential
   * stops working at once; the computer must enroll again with the new token (`sudo aictl cloud enroll`).
   */
  async replaceCredential(nodeId: string, actorUserId: string): Promise<ReplacedCredential> {
    const node = await this.requireLive(nodeId);
    const previousId = node.nodeCredentialId;
    const previous = previousId ? await this.prisma.nodeCredential.findUnique({ where: { id: previousId }, select: { id: true, tokenPrefix: true } }) : null;

    const minted = await this.credentials.createCredential(actorUserId, { name: `localai-${nodeId}` });
    // Bind the new one before revoking the old one, so the node is never left with no valid binding if the revoke fails.
    await this.prisma.localaiNode.update({ where: { id: node.id }, data: { nodeCredentialId: minted.id } });
    await this.record(AUDIT_NODE_CREDENTIAL_MINTED, actorUserId, nodeId, { credentialId: minted.id, tokenPrefix: minted.tokenPrefix });

    if (previous) {
      await this.revokeQuietly(previous.id);
      await this.record(AUDIT_NODE_CREDENTIAL_REVOKED, actorUserId, nodeId, { credentialId: previous.id, tokenPrefix: previous.tokenPrefix });
    }
    return {
      nodeId,
      token: minted.token,
      tokenPrefix: minted.tokenPrefix,
      credentialId: minted.id,
      createdAt: minted.createdAt,
      reenroll: [...REENROLL_STEPS],
    };
  }

  /**
   * Retires a node for good: the typed `confirm` must equal the node id (400 otherwise, nothing changes). Revokes its
   * credential, deletes its stored llama key, sets the phase `retired`, turns the gateway off and cancels the commands
   * still queued. A node id is never reused.
   */
  async retire(nodeId: string, confirm: string, actorUserId: string | null): Promise<RetiredNode> {
    const node = await this.requireLive(nodeId);
    if (confirm !== node.nodeId) {
      throw new BadRequestException({ message: 'Type the node id exactly to retire it', details: { reason: 'confirm_mismatch' } });
    }
    const now = new Date();
    const { count } = await this.prisma.$transaction(async (tx) => {
      const cancelled = await tx.localaiCommand.updateMany({ where: { nodeId: node.id, status: 'queued' }, data: { status: 'cancelled', finishedAt: now } });
      await tx.localaiNode.update({ where: { id: node.id }, data: { phase: 'retired', gatewayEnabled: false, retiredAt: now, nodeCredentialId: null } });
      return cancelled;
    });

    // After the commit: the credential and the key are separate stores; a failure of either is logged by name and the
    // node stays retired (a retired node is refused by the binding guard whatever its credential says).
    if (node.nodeCredentialId) {
      const credential = await this.prisma.nodeCredential.findUnique({ where: { id: node.nodeCredentialId }, select: { id: true, tokenPrefix: true } });
      if (credential) {
        await this.revokeQuietly(credential.id);
        await this.record(AUDIT_NODE_CREDENTIAL_REVOKED, actorUserId, nodeId, { credentialId: credential.id, tokenPrefix: credential.tokenPrefix });
      }
    }
    try {
      await this.keys.deleteKey(nodeId);
    } catch (error) {
      this.logger.error(`deleting the stored key of ${nodeId} failed (${error instanceof Error ? error.name : 'error'})`);
    }
    await this.record(AUDIT_NODE_RETIRED, actorUserId, nodeId, { cancelledCommands: count });
    return { nodeId, phase: 'retired', cancelledCommands: count };
  }

  private async requireLive(nodeId: string): Promise<LocalaiNode> {
    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId } });
    if (!node) throw new NotFoundException({ message: 'No Local AI node has this id' });
    if (node.phase === 'retired') {
      throw new ConflictException({ message: 'This node is retired. A retired node cannot be changed', details: { code: 'LAI-CLD-035', reason: 'node_retired' } });
    }
    return node;
  }

  /** Revoking an already revoked credential is a 404 in EAB; here it is a no-op. */
  private async revokeQuietly(credentialId: string): Promise<void> {
    try {
      await this.credentials.revokeAnyCredential(credentialId);
    } catch (error) {
      if (error instanceof NotFoundException) return;
      throw error;
    }
  }

  private async record(action: string, actorUserId: string | null, nodeId: string, meta: Record<string, string | number | boolean | null>): Promise<void> {
    try {
      await this.audit.record({ action, actorUserId, targetType: AUDIT_NODE_TARGET, targetId: nodeId, meta });
    } catch (error) {
      this.logger.error(`recording ${action} for ${nodeId} failed (${error instanceof Error ? error.name : 'error'})`);
    }
  }
}
