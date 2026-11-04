// The database-backup slice: scheduled and on-demand `pg_dump` backups streamed
// straight into object storage, retention, and a guarded restore, with the
// admin Database Backup page. Needs `pg_dump`/`pg_restore` in the API image
// (the starter's Dockerfile installs the pinned client) and a direct database
// connection (a transaction-mode pooler rejects the bypass option the dump
// sets). `DB_BACKUP_SCHEDULE_ENABLED=false` turns this process's timer off.
import type { SystemSettingsNamespace } from '@marinoscar/platform-api/settings';

import type { ApiSlice } from '../slices/slice';

export const dbBackupSlice: ApiSlice = {
  id: 'db-backup',
  permissionSlices: ['db-backup'],
  contribute: () => {
    const backup = require('@marinoscar/platform-api/db-backup') as typeof import('@marinoscar/platform-api/db-backup');
    const { DB_BACKUP_NOTIFICATIONS } = require('./db-backup.notifications') as typeof import('./db-backup.notifications');
    return {
      systemSettings: [backup.DATABASE_BACKUP_SYSTEM_SETTINGS as SystemSettingsNamespace],
      notifications: DB_BACKUP_NOTIFICATIONS,
      storagePrefixes: [backup.DB_BACKUP_KEY_PREFIX],
    };
  },
  userData: () => ({
    // Backups are the factory reset's undo: their jobs survive it.
    keepJobsReferencedBy: [{ model: 'DatabaseBackupRun', field: 'jobId' }],
  }),
  modules: () => {
    const { DbBackupModule } = require('./db-backup.config') as typeof import('./db-backup.config');
    return [DbBackupModule];
  },
};
