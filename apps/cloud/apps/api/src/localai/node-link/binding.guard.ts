// LocalaiNodeBindingGuard: every `/api/nodes/{id}/localai/*` route runs it after EAB's `JwtAuthGuard`
// (docs/specs/cloud-app.md section 4.2; interim for EAB seam request SR-2, section 3.12).
//
// WHY IT EXISTS. EAB confines a `nod_` credential to `/api/nodes/*` but scopes it to its OWNER, not to one node, and
// does not tell the route which credential it was. So a leaked credential could speak for every node its owner has.
// This guard binds one credential to exactly one lab node, and it is the only place that loads the node row.
//
// FAIL CLOSED. The request is admitted only by reaching the last line. Every missing input (no credential kind, no
// bearer, no user, an id that is not a uuid, a credential that vanished or was revoked between EAB's check and ours)
// is a refusal. Audit and notification failures are logged and never change the answer.
//
// SECRETS. The raw bearer is read once, hashed with the same sha256 hex digest EAB stores in
// `node_credentials.token_hash`, and dropped; it is never logged, never put in an error and never compared in
// application code. The comparison that matters is the database's unique-index lookup by digest (a 256-bit digest of
// a 256-bit secret: nothing to time), after which only ids, which are not secrets, are compared.
import { createHash } from 'node:crypto';
import {
  type CanActivate,
  type ExecutionContext,
  ForbiddenException,
  Inject,
  Injectable,
  Logger,
  NotFoundException,
  ServiceUnavailableException,
  UnauthorizedException,
} from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import type { LocalaiNode, NodeCredential, WorkerNode } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { decideBinding } from './binding.decision';
import { bindingRefusal, LAI_CLD_WRONG_NODE } from './node-link.errors';
import { LOCALAI_NODE_NOTIFIER, type LocalaiNodeNotifier } from './node-link.notifier';
import type { NodeLinkRequest } from './node-link.types';

/** Audit actions (permanent strings, spec section 3.5). */
export const AUDIT_NODE_ENROLLED = 'localai.node.enrolled';
export const AUDIT_NODE_BINDING_REFUSED = 'localai.node.binding_refused';

const BEARER = 'Bearer ';
const NODE_TOKEN_PREFIX = 'nod_';
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
/** A write loses its race at most to one other contact of the same node; three rounds is plenty. */
const MAX_WRITE_ROUNDS = 3;

/** The digest EAB stores for a `nod_` token (`NodeCredentialService` private `hash`: sha256, hex). Pinned by a test. */
export function nodeTokenDigest(rawToken: string): string {
  return createHash('sha256').update(rawToken).digest('hex');
}

/** The raw `nod_` token of an `Authorization` header, or null when it is not one (same slicing as EAB's guard). */
export function nodeBearerOf(header: string | string[] | undefined): string | null {
  if (typeof header !== 'string' || !header.startsWith(BEARER + NODE_TOKEN_PREFIX)) return null;
  return header.slice(BEARER.length);
}

@Injectable()
export class LocalaiNodeBindingGuard implements CanActivate {
  private readonly logger = new Logger(LocalaiNodeBindingGuard.name);

  constructor(
    private readonly prisma: PrismaService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(LOCALAI_NODE_NOTIFIER) private readonly notifier: LocalaiNodeNotifier,
  ) {}

  async canActivate(context: ExecutionContext): Promise<boolean> {
    const request = context.switchToHttp().getRequest<NodeLinkRequest>();
    const userId = request.user?.id;

    // 1. Sessions and personal access tokens cannot speak for a node.
    if (request.authCredential?.kind !== 'node' || !userId) {
      throw bindingRefusal(LAI_CLD_WRONG_NODE, 'node_credential_required');
    }

    // 2. Which credential is this? Re-derived from the bearer (EAB does not expose the id).
    const raw = nodeBearerOf(request.headers.authorization);
    if (raw === null) throw bindingRefusal(LAI_CLD_WRONG_NODE, 'node_credential_required');
    const credential = await this.prisma.nodeCredential.findUnique({ where: { tokenHash: nodeTokenDigest(raw) } });
    if (!credential || !isLive(credential) || credential.userId !== userId) {
      // Same answer EAB gives for an unknown, revoked or expired credential.
      throw new UnauthorizedException('Invalid or expired node credential');
    }

    // 3. The worker node in the URL exists and is the caller's (EAB's `assertOwnership` semantics).
    const workerNodeId = request.params?.id;
    if (typeof workerNodeId !== 'string' || !UUID.test(workerNodeId)) {
      throw notFoundWorker(String(workerNodeId).slice(0, 64));
    }
    const worker = await this.prisma.workerNode.findUnique({ where: { id: workerNodeId } });
    if (!worker) throw notFoundWorker(workerNodeId);
    if (worker.createdById !== userId) throw forbiddenWorker(workerNodeId);

    // 4 to 6. The Local AI node, the binding, and the writes that follow a first or a repeated contact.
    const node = await this.admit(request, credential, worker);
    // 7. Handlers receive the verified row.
    request.localaiNode = node;
    return true;
  }

  private async admit(request: NodeLinkRequest, credential: NodeCredential, worker: WorkerNode): Promise<LocalaiNode> {
    let node = await this.prisma.localaiNode.findUnique({ where: { nodeId: worker.name } });

    for (let round = 0; round < MAX_WRITE_ROUNDS; round += 1) {
      const decision = decideBinding({ credentialId: credential.id, workerNodeId: worker.id, node });
      if (decision.outcome === 'refuse') {
        if (decision.audit) await this.auditRefusal(request.user?.id ?? null, credential, worker.name);
        throw bindingRefusal(decision.details.code, decision.details.reason);
      }
      // `node` is non-null here: a null row is always a refusal.
      const current = node as LocalaiNode;
      if (!decision.enroll && !decision.relink) return current;

      // One conditional write: it applies only while the row is still the one we decided on (same phase, same bound
      // credential), so a concurrent first call, a retire or a credential rotation turns it into count 0.
      const data = { workerNodeId: worker.id, ...(decision.enroll ? { phase: 'enrolled' as const } : {}) };
      const written = await this.prisma.localaiNode.updateMany({
        where: { id: current.id, phase: current.phase, nodeCredentialId: credential.id },
        data,
      });
      if (written.count === 1) {
        const updated: LocalaiNode = { ...current, ...data };
        // Exactly the caller whose write changed the row announces the enrollment.
        if (decision.enroll) await this.announceEnrolled(request.user?.id ?? null, updated, worker);
        return updated;
      }
      node = await this.prisma.localaiNode.findUnique({ where: { id: current.id } });
    }
    // The row kept changing under us: do not guess.
    throw new ServiceUnavailableException('The node record is changing; try again');
  }

  /** After the enrolling write committed (outside any transaction): audit, then notify. Neither can refuse the call. */
  private async announceEnrolled(actorUserId: string | null, node: LocalaiNode, worker: WorkerNode): Promise<void> {
    try {
      await this.audit.record({
        action: AUDIT_NODE_ENROLLED,
        actorUserId,
        targetType: 'localai_node',
        targetId: node.nodeId,
        meta: { workerNodeId: worker.id, agentVersion: node.agentVersion ?? worker.cliVersion ?? null },
      });
    } catch (error) {
      this.logger.error(`audit ${AUDIT_NODE_ENROLLED} failed for node_id=${node.nodeId}: ${describe(error)}`);
    }
    try {
      await this.notifier.nodeEnrolled({ nodeId: node.nodeId, displayName: node.displayName, workerNodeId: worker.id });
    } catch (error) {
      this.logger.error(`notification localai.node.enrolled failed for node_id=${node.nodeId}: ${describe(error)}`);
    }
  }

  private async auditRefusal(actorUserId: string | null, credential: NodeCredential, claimedNodeId: string): Promise<void> {
    // The credential PREFIX (`nod_` plus a few characters) identifies it for the operator; the token never appears.
    this.logger.warn(`binding refused: credential=${credential.tokenPrefix} claimed_node_id=${claimedNodeId}`);
    try {
      await this.audit.record({
        action: AUDIT_NODE_BINDING_REFUSED,
        actorUserId,
        targetType: 'localai_node',
        targetId: claimedNodeId,
        meta: { credentialPrefix: credential.tokenPrefix, claimedNodeId },
      });
    } catch (error) {
      this.logger.error(`audit ${AUDIT_NODE_BINDING_REFUSED} failed for node_id=${claimedNodeId}: ${describe(error)}`);
    }
  }
}

/** EAB's wording for a worker node that does not exist. No Local AI code: it is EAB's own refusal. */
function notFoundWorker(nodeId: string): NotFoundException {
  return new NotFoundException({ message: `Worker node ${nodeId} was not found.`, details: { nodeId } });
}

/** EAB's wording for a worker node that belongs to someone else. */
function forbiddenWorker(nodeId: string): ForbiddenException {
  return new ForbiddenException({
    message: `Worker node ${nodeId} belongs to another user. Check that this credential and this node id came from the same registration.`,
    details: { nodeId },
  });
}

function isLive(credential: NodeCredential): boolean {
  if (credential.revokedAt !== null) return false;
  return credential.expiresAt === null || credential.expiresAt > new Date();
}

/** Error text only: never the error object, whose message could in principle embed request data. */
function describe(error: unknown): string {
  return error instanceof Error ? error.name : 'unknown error';
}
