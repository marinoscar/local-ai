// =============================================================================
// Database backup notifications
// =============================================================================
//
// Pure data: the two events the db-backup slice raises. The slice owns the
// event keys and template names (`DB_BACKUP_NOTIFICATION_EVENTS`,
// `DB_BACKUP_EMAIL_TEMPLATES`); the definitions are registered through the
// notifications slice by `./db-backup.slice.ts`. An event key is persisted
// (preferences, delivery rows): never rename one.
// =============================================================================

import { DB_BACKUP_EMAIL_TEMPLATES, DB_BACKUP_NOTIFICATION_EVENTS } from '@marinoscar/platform-api/db-backup';
import {
  backupFailedBrowserTemplate,
  restoreCompletedBrowserTemplate,
  type NotificationRegistration,
} from '@marinoscar/platform-api/notifications';

export const DB_BACKUP_NOTIFICATIONS: readonly NotificationRegistration[] = [
  {
    event: {
      key: DB_BACKUP_NOTIFICATION_EVENTS.BACKUP_FAILED,
      label: 'Database backup failed',
      description:
        'Sent when a database backup run fails, or stops heartbeating and is given up on. The deployment has one fewer recovery point than it thinks.',
      channels: ['email', 'browser'],
      defaultEnabled: true,
    },
    emailTemplate: DB_BACKUP_EMAIL_TEMPLATES.BACKUP_FAILED,
    browserTemplate: backupFailedBrowserTemplate,
  },
  {
    event: {
      key: DB_BACKUP_NOTIFICATION_EVENTS.RESTORE_COMPLETED,
      label: 'Database restored',
      description:
        'Sent when a database restore finishes and the restored copy becomes the live database. This cannot be turned off.',
      channels: ['email', 'browser'],
      defaultEnabled: true,
      // Silence is itself the risk: a restore discards every write made after
      // the archive was taken, and the process that did it exits afterwards.
      mandatory: true,
    },
    emailTemplate: DB_BACKUP_EMAIL_TEMPLATES.RESTORE_COMPLETED,
    browserTemplate: restoreCompletedBrowserTemplate,
  },
];
