// The user-data slice's one host port (issue #880): the bypass client.
//
// A user's rows live in every organization they belong to, and offboarding and
// the factory reset cross organizations by definition, so row-level security
// must not hide a row from the purge. `PrismaSystemService.runAsSystem` and
// `.asSystem` already have the port's shape; the adapter only narrows the
// reasons to the two the slice uses.

import { Injectable, Module } from '@nestjs/common';
import { USER_DATA_DB, type UserDataDbPort, type UserDataSystemReason } from '@marinoscar/platform-api/user-data';

import { PrismaSystemService } from '../../prisma/prisma-system.service';

/** `USER_DATA_DB`: the bypass client, by reason (`purge` or `admin-aggregate`). */
@Injectable()
export class UserDataDbAdapter implements UserDataDbPort {
  constructor(private readonly prisma: PrismaSystemService) {}

  runAsSystem<R>(reason: UserDataSystemReason, fn: (tx: any) => Promise<R>, options?: { timeout?: number }): Promise<R> {
    return this.prisma.runAsSystem(reason, fn, options ?? {});
  }

  system(reason: UserDataSystemReason): any {
    return this.prisma.asSystem(reason);
  }
}

@Module({
  providers: [UserDataDbAdapter, { provide: USER_DATA_DB, useExisting: UserDataDbAdapter }],
  exports: [USER_DATA_DB],
})
export class UserDataHostModule {}
