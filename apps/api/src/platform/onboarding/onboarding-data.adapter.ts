// =============================================================================
// The onboarding slice's ONBOARDING_DATA port, bound to the reference app
// =============================================================================
//
// Every method is a READ:
//
//   - `readUserSettingsValue` SELECTs the row; it never goes through
//     `UserSettingsService.getSettings`, which creates a default row on first
//     read, so `GET /api/onboarding` leaves the database unchanged for a user
//     with no settings row (`test/onboarding/onboarding-read-only.db.spec.ts`).
//   - `orgInviteProgress` reads memberships and invites of the CALLER'S
//     active organization (the id comes from the principal, never the request).
//   - `queryAggregate` runs the metrics' one statement: aggregates over
//     `users`, `user_settings` and `push_subscriptions` (no RLS table), its
//     values bound as positional parameters. Listed in
//     `test/prisma/raw-sql-allowlist.ts`.
// =============================================================================

import { Injectable } from '@nestjs/common';
import type { OnboardingDataPort } from '@marinoscar/platform-api/onboarding';

import { PrismaService } from '../../prisma/prisma.service';

@Injectable()
export class OnboardingDataAdapter implements OnboardingDataPort {
  constructor(private readonly prisma: PrismaService) {}

  async readUserSettingsValue(userId: string): Promise<unknown> {
    const row = await this.prisma.userSettings.findUnique({ where: { userId }, select: { value: true } });
    return row?.value ?? null;
  }

  countAllowlistEntriesExcept(exceptEmail: string | null): Promise<number> {
    return this.prisma.allowedEmail.count({
      where: exceptEmail ? { NOT: { email: { equals: exceptEmail, mode: 'insensitive' } } } : {},
    });
  }

  async hasPushSubscription(userId: string): Promise<boolean> {
    return (await this.prisma.pushSubscription.findFirst({ where: { userId }, select: { id: true } })) !== null;
  }

  async orgInviteProgress(orgId: string, userId: string): Promise<{ otherMembers: number; pendingInvites: number }> {
    const [otherMembers, pendingInvites] = await Promise.all([
      this.prisma.membership.count({ where: { orgId, status: 'active', NOT: { userId } } }),
      this.prisma.invite.count({ where: { orgId, status: 'pending' } }),
    ]);
    return { otherMembers, pendingInvites };
  }

  queryAggregate<T>(sql: string, values: readonly unknown[]): Promise<T[]> {
    return this.prisma.$queryRawUnsafe<T[]>(sql, ...values);
  }
}
