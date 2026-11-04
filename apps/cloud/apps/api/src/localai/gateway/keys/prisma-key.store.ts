import { Injectable } from '@nestjs/common';

import { PrismaService } from '../../../prisma/prisma.service';
import type { KeyChanges, KeyListFilter, KeyRecord, KeyStore, NewKey } from './key.store';

const WITH_OWNER = { user: { select: { email: true, displayName: true } } } as const;

@Injectable()
export class PrismaKeyStore implements KeyStore {
  constructor(private readonly prisma: PrismaService) {}

  insert(data: NewKey): Promise<KeyRecord> {
    return this.prisma.localaiGatewayKey.create({ data, include: WITH_OWNER });
  }

  findById(id: string): Promise<KeyRecord | null> {
    return this.prisma.localaiGatewayKey.findUnique({ where: { id }, include: WITH_OWNER });
  }

  list(filter: KeyListFilter): Promise<KeyRecord[]> {
    return this.prisma.localaiGatewayKey.findMany({
      where: { ...(filter.ownerUserId !== undefined ? { userId: filter.ownerUserId } : {}), ...(filter.includeRevoked ? {} : { revokedAt: null }) },
      orderBy: [{ createdAt: 'desc' }, { id: 'asc' }],
      include: WITH_OWNER,
    });
  }

  async update(id: string, changes: KeyChanges): Promise<KeyRecord | null> {
    const result = await this.prisma.localaiGatewayKey.updateMany({ where: { id, revokedAt: null }, data: changes });
    if (result.count === 0) return null;
    return this.findById(id);
  }

  async revoke(id: string, at: Date): Promise<boolean> {
    const result = await this.prisma.localaiGatewayKey.updateMany({ where: { id, revokedAt: null }, data: { revokedAt: at } });
    return result.count > 0;
  }

  async userExists(userId: string): Promise<boolean> {
    return (await this.prisma.user.count({ where: { id: userId } })) > 0;
  }
}
