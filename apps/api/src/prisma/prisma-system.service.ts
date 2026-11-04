import { Injectable, Logger, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';
import { APP_SLUG } from '@app/shared';
import { Prisma, PrismaClient } from '@prisma/client';
import { PrismaPg } from '@prisma/adapter-pg';
import {
  buildDatabaseUrl,
  runAsSystem,
  systemScopeExtension,
  type RlsTransactionOptions,
  type SystemAccessReason,
} from '@marinoscar/platform-api/core';

/** Connections the system pool may hold. Small: system work is batch work. */
export const SYSTEM_POOL_MAX = 4;

/** `application_name` of every system session. */
export const SYSTEM_APPLICATION_NAME = `${APP_SLUG}-system`;

/**
 * The bypass client: a second pool whose every operation lifts row-level
 * security for its own transaction (`app.rls_bypass`, set transaction-locally).
 * The user-data slice needs it because a user's rows sit in every organization
 * they belong to; bind it to a slice's system port (`USER_DATA_DB`,
 * `src/platform/user-data/`) and inject it nowhere else. Every access names a
 * closed `SystemAccessReason`.
 */
@Injectable()
export class PrismaSystemService extends PrismaClient implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger(PrismaSystemService.name);

  constructor() {
    super({
      adapter: new PrismaPg({
        connectionString: buildDatabaseUrl(process.env),
        max: SYSTEM_POOL_MAX,
        application_name: SYSTEM_APPLICATION_NAME,
      }),
    });
  }

  async onModuleInit(): Promise<void> {
    await this.$connect();
    this.logger.log(`System database pool connected (${SYSTEM_APPLICATION_NAME}, max ${SYSTEM_POOL_MAX})`);
  }

  async onModuleDestroy(): Promise<void> {
    await this.$disconnect();
  }

  /** A client whose every single statement lifts row-level security. Name the reason. */
  asSystem(reason: SystemAccessReason) {
    this.logger.debug(`System database access: ${reason}`);
    return this.$extends(systemScopeExtension(this, reason));
  }

  /** One interactive transaction with the bypass flag set, handing `fn` the plain transaction client. */
  runAsSystem<R>(
    reason: SystemAccessReason,
    fn: (tx: Prisma.TransactionClient) => Promise<R>,
    options: RlsTransactionOptions = {},
  ): Promise<R> {
    this.logger.debug(`System database access: ${reason}`);
    return runAsSystem(this, reason, fn as (tx: unknown) => Promise<R>, options);
  }
}
