import { Global, Injectable, Logger, Module, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';
import { Prisma, PrismaClient } from '@prisma/client';
import { PrismaPg } from '@prisma/adapter-pg';
import { buildDatabaseUrl, runInOrg, userScopeExtension, type Scope } from '@marinoscar/platform-api/core';

// The registry of user-owned models fills BEFORE the first scoped client is
// built (src/platform/registrations.ts: the platform's models, then the app's).
import '../platform/registrations';
import { PrismaSystemService } from './prisma-system.service';

/**
 * The app's one Prisma client, generated from the composed schema
 * (`prisma/schema/`, platform models plus `prisma/fragments/`). Bound to
 * core's `PLATFORM_PRISMA` port by `src/platform/platform.ts`, so every
 * packaged slice uses this connection pool.
 */
@Injectable()
export class PrismaService extends PrismaClient implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger(PrismaService.name);

  constructor() {
    super({ adapter: new PrismaPg(buildDatabaseUrl(process.env)) });
  }

  async onModuleInit(): Promise<void> {
    await this.$connect();
    this.logger.log('Database connected');
  }

  async onModuleDestroy(): Promise<void> {
    await this.$disconnect();
  }

  /** A client confined to `scope.userId` on every registered user-owned model (core's scoped data access). */
  forUser(scope: Scope) {
    return this.$extends(userScopeExtension(scope));
  }

  /** One transaction in an organization's row-level-security scope (the settings slice's `SETTINGS_DATA` port). */
  runInOrg<R>(orgId: string, fn: (tx: Prisma.TransactionClient) => Promise<R>, opts: { userId?: string } = {}): Promise<R> {
    return runInOrg(this, { orgId, ...opts }, fn as (tx: unknown) => Promise<R>);
  }
}

/** Global, like the database: every module injects `PrismaService` without importing this. */
@Global()
@Module({ providers: [PrismaService, PrismaSystemService], exports: [PrismaService, PrismaSystemService] })
export class PrismaModule {}
