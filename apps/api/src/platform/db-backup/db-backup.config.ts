// The database-backup slice, configured once: backups, restores, their four
// job types, the admin routes and three Doctor checks. The deployment mode, the
// scheduler switch (`DB_BACKUP_SCHEDULE_ENABLED`) and the backup policy stay the
// deployment's and the administrator's: no option is passed that they decide.
// The app passes its name (the archive key's slug), its version resolver
// (`app_version` on every run), its host ports and the tables whose rows must
// survive a restore.
import { APP_NAME } from '@app/shared';
import { DbBackupModule as PlatformDbBackupModule } from '@marinoscar/platform-api/db-backup';
import { resolveApiVersion } from '@marinoscar/platform-api/host';

import { APP_RESTORE_CARRY_OVERS } from './restore-carry-over';
import { DbBackupHostModule } from './db-backup-host.module';

export const DbBackupModule = PlatformDbBackupModule.forRoot({
  appName: APP_NAME,
  appVersion: () => resolveApiVersion(__dirname),
  extraCarryOver: APP_RESTORE_CARRY_OVERS,
  imports: [DbBackupHostModule],
});
