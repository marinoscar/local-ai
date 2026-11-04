// The only raw SQL of the command plane: the claim (docs/specs/cloud-app.md section 4.4). It is raw because Prisma
// cannot express `FOR UPDATE SKIP LOCKED`; it is allowlisted for the `user-owned-data` conformance suite in
// `localai.raw-sql.ts`. Every value is a bound parameter; nothing request-derived is interpolated into the text.
//
// What the claim guarantees, and how:
//   * ONE claimer at a time per node. The transaction first locks the node's own row (`FOR NO KEY UPDATE`, which
//     does not block the foreign-key checks of an enqueue), so two claims for a node, from two API processes or two
//     requests with the same credential, run one after the other; the second sees the first one's `running` row.
//   * ONE running command per node. The pick refuses when the node already has a `running` command whose lease has
//     not passed (a node finishes one command before it takes the next).
//   * At most once. The pick locks the row (`FOR UPDATE SKIP LOCKED`) and the same transaction turns it `running`
//     with a fresh token, so no row can be handed to two claimers.
//   * FIFO per node by `created_at`; an expired `queued` row is never picked; only the requested types.
// The transaction lasts a few milliseconds. It is NEVER held across the long-poll wait.
import { Injectable } from '@nestjs/common';
import type { LocalaiCommand } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';

/** What the repository needs to claim: the node, the full types, the lease of each type and "now". */
export interface ClaimAttempt {
  nodeId: string;
  /** Full types (`localai.model.load`), already filtered to the known ones. */
  types: readonly string[];
  now: Date;
  /** The lease for a picked row's type, in milliseconds. */
  leaseMsOf(type: string): number;
}

/** The claim as the service sees it: injected, so the long-poll logic is unit-tested against a fake. */
export abstract class CommandClaimRepository {
  /** Claims the oldest eligible command, or returns null when there is none (or the node is busy). */
  abstract claimNext(attempt: ClaimAttempt): Promise<LocalaiCommand | null>;
}

@Injectable()
export class PrismaCommandClaimRepository extends CommandClaimRepository {
  constructor(private readonly prisma: PrismaService) {
    super();
  }

  async claimNext(attempt: ClaimAttempt): Promise<LocalaiCommand | null> {
    if (attempt.types.length === 0) return null;
    return this.prisma.$transaction(async (tx) => {
      // Serialize the claimers of this node.
      await tx.$queryRaw`SELECT id FROM localai_nodes WHERE id = ${attempt.nodeId}::uuid FOR NO KEY UPDATE`;

      const picked = await tx.$queryRaw<Array<{ id: string; type: string }>>`
        SELECT c.id, c.type
          FROM localai_commands c
         WHERE c.node_id = ${attempt.nodeId}::uuid
           AND c.status = 'queued'
           AND c.expires_at > ${attempt.now}::timestamptz
           AND c.type = ANY(${[...attempt.types]}::text[])
           AND NOT EXISTS (
             SELECT 1 FROM localai_commands r
              WHERE r.node_id = c.node_id AND r.status = 'running' AND r.lease_expires_at > ${attempt.now}::timestamptz)
         ORDER BY c.created_at ASC
         FOR UPDATE SKIP LOCKED
         LIMIT 1`;
      const row = picked[0];
      if (!row) return null;

      const leaseExpiresAt = new Date(attempt.now.getTime() + attempt.leaseMsOf(row.type));
      // A fresh token per claim; `gen_random_uuid()` is core in PostgreSQL 13 and later.
      await tx.$executeRaw`
        UPDATE localai_commands
           SET status = 'running', attempts = attempts + 1, claimed_at = ${attempt.now}::timestamptz,
               claim_token = gen_random_uuid(), lease_expires_at = ${leaseExpiresAt}::timestamptz
         WHERE id = ${row.id}::uuid`;
      return tx.localaiCommand.findUniqueOrThrow({ where: { id: row.id } });
    });
  }
}
