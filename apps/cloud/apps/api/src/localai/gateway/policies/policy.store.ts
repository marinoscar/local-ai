// The persistence port of the model policies. `PrismaPolicyStore` is the real one; the unit tests use an in-memory fake.
// The write is ONE transaction that first takes an advisory lock, so two policy writes cannot both pass the "alias is
// globally unique" check (docs/specs/gateway.md section 3.6: checked in the service, not by a database constraint).
import { Injectable } from '@nestjs/common';

import { PrismaService } from '../../../prisma/prisma.service';

export interface PolicyRecord {
  modelId: string;
  enabled: boolean;
  aliases: string[];
  onDemandLoad: boolean;
  loadWaitS: number;
  firstByteTimeoutS: number | null;
  preferredNodes: string[];
  updatedById: string | null;
  updatedAt: Date;
}

export type PolicyData = Omit<PolicyRecord, 'modelId' | 'updatedById' | 'updatedAt'>;

export const GATEWAY_POLICY_STORE = Symbol('LOCALAI_GATEWAY_POLICY_STORE');

export interface PolicyStore {
  list(): Promise<PolicyRecord[]>;
  /**
   * In one serialised transaction: calls `guard` with the policy being replaced (or null) and every OTHER policy (it
   * throws to refuse the write), then upserts. Answers the row before and after.
   */
  upsert(
    modelId: string,
    data: PolicyData,
    updatedById: string | null,
    guard: (others: readonly PolicyRecord[], before: PolicyRecord | null) => void,
  ): Promise<{ before: PolicyRecord | null; after: PolicyRecord }>;
}

/** Any int64; the same key on every replica, so the writes queue behind one another. */
const POLICY_LOCK_KEY = 7_204_001;

@Injectable()
export class PrismaPolicyStore implements PolicyStore {
  constructor(private readonly prisma: PrismaService) {}

  list(): Promise<PolicyRecord[]> {
    return this.prisma.localaiModelPolicy.findMany({ orderBy: { modelId: 'asc' } });
  }

  async upsert(
    modelId: string,
    data: PolicyData,
    updatedById: string | null,
    guard: (others: readonly PolicyRecord[], before: PolicyRecord | null) => void,
  ): Promise<{ before: PolicyRecord | null; after: PolicyRecord }> {
    return this.prisma.$transaction(async (tx) => {
      await tx.$executeRaw`SELECT pg_advisory_xact_lock(${POLICY_LOCK_KEY})`;
      const all = await tx.localaiModelPolicy.findMany();
      const before = all.find((row) => row.modelId === modelId) ?? null;
      guard(
        all.filter((row) => row.modelId !== modelId),
        before,
      );
      const after = await tx.localaiModelPolicy.upsert({
        where: { modelId },
        create: { modelId, ...data, updatedById },
        update: { ...data, updatedById },
      });
      return { before, after };
    });
  }
}
