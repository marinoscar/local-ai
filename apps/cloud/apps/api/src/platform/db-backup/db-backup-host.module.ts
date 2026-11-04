// =============================================================================
// The reference app's bindings of the db-backup slice's host ports
// =============================================================================
//
// `@Global()`, passed to `DbBackupModule.forRoot({ imports })`
// (./db-backup.config.ts):
//
//   DB_BACKUP_NOTIFIER         -> NotificationsService   (the two db_backup.* events)
//   DB_BACKUP_MAINTENANCE      -> MaintenanceModeService (the swap's in-memory gate)
//   DB_BACKUP_METRICS          -> AppMetricsService      (the app.backup.* instruments)
//   DB_BACKUP_DEPLOYMENT_MODE  -> DeploymentModeService  (DEPLOYMENT_MODE, #685)
//   DB_BACKUP_SYSTEM_DATA      -> PrismaSystemService    (the Doctor's RLS count)
//
// The tenant client is the core port `PLATFORM_PRISMA` (`PlatformHostModule`,
// bound to `PrismaService`). The `backup.rls-bypass` Doctor check counts every
// organization's rows through the bypass client, under the `doctor` reason.
// =============================================================================

import { Global, Module } from '@nestjs/common';
import {
  DB_BACKUP_DEPLOYMENT_MODE,
  DB_BACKUP_MAINTENANCE,
  DB_BACKUP_METRICS,
  DB_BACKUP_NOTIFIER,
  DB_BACKUP_SYSTEM_DATA,
} from '@marinoscar/platform-api/db-backup';

import { AppMetricsService, DeploymentModeService, MaintenanceModeService } from '@marinoscar/platform-api/host';
import { NotificationsModule } from '../notifications/notifications.config';
import { NotificationsService } from '@marinoscar/platform-api/notifications';
import { PrismaSystemService } from '../../prisma/prisma-system.service';

const BINDINGS = [
  { provide: DB_BACKUP_NOTIFIER, useExisting: NotificationsService },
  { provide: DB_BACKUP_MAINTENANCE, useExisting: MaintenanceModeService },
  { provide: DB_BACKUP_METRICS, useExisting: AppMetricsService },
  { provide: DB_BACKUP_DEPLOYMENT_MODE, useExisting: DeploymentModeService },
  { provide: DB_BACKUP_SYSTEM_DATA, useExisting: PrismaSystemService },
];

@Global()
@Module({
  imports: [NotificationsModule],
  providers: BINDINGS,
  exports: BINDINGS.map((binding) => binding.provide),
})
export class DbBackupHostModule {}
