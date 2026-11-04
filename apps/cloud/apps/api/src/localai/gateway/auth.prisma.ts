// The Prisma adapters of the gateway authenticator's ports.
import { Injectable } from '@nestjs/common';
import { PRINCIPAL_USER_INCLUDE, PrincipalFactory } from '@marinoscar/platform-api/identity';

import { PrismaService } from '../../prisma/prisma.service';
import type { GatewayKeyRecord, GatewayKeyStore, OwnerAccess, OwnerAccessReader } from './auth';

@Injectable()
export class PrismaGatewayKeyStore implements GatewayKeyStore {
  constructor(private readonly prisma: PrismaService) {}

  async findByTokenHash(tokenHash: string): Promise<GatewayKeyRecord | null> {
    const row = await this.prisma.localaiGatewayKey.findUnique({
      where: { tokenHash },
      select: {
        id: true,
        userId: true,
        name: true,
        kind: true,
        tokenHash: true,
        tokenPrefix: true,
        allowedModels: true,
        rpmLimit: true,
        concurrentLimit: true,
        dailyTokenBudget: true,
        expiresAt: true,
        revokedAt: true,
      },
    });
    return row;
  }

  async touchLastUsed(keyId: string, at: Date): Promise<void> {
    // updateMany: a key deleted in the meantime is a no-op, not an error.
    await this.prisma.localaiGatewayKey.updateMany({ where: { id: keyId }, data: { lastUsedAt: at } });
  }
}

/** The owner's activity and EFFECTIVE permissions, computed by the platform's `PrincipalFactory` like any request. */
@Injectable()
export class PrismaOwnerAccessReader implements OwnerAccessReader {
  private readonly principals = new PrincipalFactory();

  constructor(private readonly prisma: PrismaService) {}

  async read(userId: string): Promise<OwnerAccess | null> {
    const user = await this.prisma.user.findUnique({ where: { id: userId }, include: PRINCIPAL_USER_INCLUDE });
    if (!user) return null;
    return { active: user.isActive, permissions: this.principals.access(user).permissions };
  }
}
