// Tables whose rows must survive a database RESTORE.
//
// A restore replaces the live database with an archive, so a row written after
// that archive was taken is gone. That is right for almost every table and wrong for a table that records something that
// happened OUTSIDE the database: say a `release_artifacts` table, one row per
// build uploaded to object storage, whose files are still in the bucket.
// Declare such a table's carry once; the restore reads its rows from the live
// database before the swap and upserts each into the promoted one after it:
//
//   export const RELEASE_ARTIFACTS_CARRY_OVER: RestoreCarryOver = Object.freeze({
//     id: 'release_artifacts',
//     order: 10, // after the four built-in carries
//     exportSql: 'SELECT * FROM release_artifacts ORDER BY created_at',
//     reinsertSql:
//       'INSERT INTO release_artifacts SELECT * FROM jsonb_populate_record(NULL::release_artifacts, $1::jsonb) ' +
//       'ON CONFLICT (id) DO UPDATE SET storage_key = EXCLUDED.storage_key',
//   });
//
// None is registered here: a carry whose `exportSql` fails is logged as
// CRITICAL on every restore. Append yours.
import type { RestoreCarryOver } from '@marinoscar/platform-api/db-backup';

export const APP_RESTORE_CARRY_OVERS: readonly RestoreCarryOver[] = [];
