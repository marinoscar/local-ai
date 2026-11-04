import { Module } from '@nestjs/common';

import { AuditController } from './audit.controller';
import { AuditService } from './audit.service';

/** The audit page's read API (`GET /api/localai/audit`, `GET /api/localai/audit/filters`). `PrismaService` is global. */
@Module({ controllers: [AuditController], providers: [AuditService] })
export class LocalaiAuditModule {}
