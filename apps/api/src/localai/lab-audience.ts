// Who a lab notification goes to: the active users who HOLD a permission, through a system role or through an active
// membership's organization role.
//
// Why this is not the platform's `notifyPermissionHolders`: that resolves holders from system roles only (`user_roles`).
// `lab:read` and `lab:operate` are ORGANIZATION permissions, held by the `contributor` and `org_admin` roles on a
// membership, so that helper would find nobody for "holders of lab:operate". The dispatcher itself (`notify`, one user at a
// time) is unchanged: preferences, the admin policy and the delivery rows all still apply to each recipient.
import { Injectable, Logger } from '@nestjs/common';

import { PrismaService } from '../prisma/prisma.service';

@Injectable()
export class LabAudience {
  private readonly logger = new Logger(LabAudience.name);

  constructor(private readonly prisma: PrismaService) {}

  /** The ids of every active user holding `permission`. A failed lookup is a log line and an empty audience, never a throw. */
  async holdersOf(permission: string): Promise<string[]> {
    const grants = { rolePermissions: { some: { permission: { name: permission } } } };
    try {
      const users = await this.prisma.user.findMany({
        where: { isActive: true, OR: [{ userRoles: { some: { role: grants } } }, { memberships: { some: { status: 'active', role: grants } } }] },
        select: { id: true },
      });
      return users.map((user) => user.id);
    } catch (error) {
      this.logger.error(`could not resolve the holders of '${permission}': ${error instanceof Error ? error.name : 'unknown error'}`);
      return [];
    }
  }
}
