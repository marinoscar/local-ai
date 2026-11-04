// The database-backup slice, on the web: the backup policy, the run history and
// the guarded restore dialog, on one page. `db_backup:read` is the string
// `GET /api/admin/db-backup/*` enforces (NOT `system_settings:read`: backup
// access can be granted without the settings document); writes and restores
// are gated inside the page on `db_backup:write` and `db_backup:restore`.
import { dbBackupAdminSections } from '@marinoscar/platform-web/db-backup/ui';
import { lazy } from 'react';

import type { WebSlice } from './slice';

const DbBackupPage = lazy(() => import('@marinoscar/platform-web/db-backup/ui'));

export const dbBackupWebSlice: WebSlice = {
  id: 'db-backup',
  routes: [{ path: 'admin/settings/db-backup', permission: 'db_backup:read', element: <DbBackupPage /> }],
  adminCards: [{ group: 'Operations', cards: dbBackupAdminSections.operations }],
  consolePermissions: ['db_backup:read'],
};
