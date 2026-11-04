-- =============================================================================
-- Backup Run <-> Job link, and tightening the single-active-run guard
-- (issue #351, epic #345)
-- =============================================================================
-- Two independent changes land together because the second is only safe once
-- the first is possible to reason about (see the second block below for why
-- they must not be split across two migrations).
--
-- 1. `database_backup_runs.job_id`: the FK from a backup run back to the
--    queue job that drove it, once the dump becomes a job (`db.backup.run`).
--    Nullable, no default, no backfill — see the block comment above
--    `DatabaseBackupRun` in prisma/schema.prisma ("jobId") for the full
--    reasoning behind `@unique` / nullable / `SetNull`, which this migration
--    intentionally does not restate.
--
-- 2. Tightening `database_backup_runs_active_uniq_idx` from a UNIQUE index on
--    the `status` COLUMN to a UNIQUE index on the CONSTANT EXPRESSION `(true)`
--    — both filtered to `status IN ('pending','running')`. See the block
--    comment above `DatabaseBackupRun` in prisma/schema.prisma
--    ("THE SINGLE-ACTIVE-RUN GUARD...") for the full story; the short version:
--    the old, column-keyed index admits one `pending` row AND one `running`
--    row AT THE SAME TIME (two active runs), which was harmless only because
--    nothing ever wrote `pending`. #351's service half makes
--    `POST /api/admin/db-backup/runs` enqueue the job and insert the run row
--    as `pending` before any worker claims it, so `pending` rows become real.
--    Keying on `(true)` instead makes every active row — whichever status it
--    holds — collide on the same key, so at most one can exist across both
--    statuses combined.
-- =============================================================================

-- AlterTable
ALTER TABLE "database_backup_runs" ADD COLUMN "job_id" UUID;

-- CreateIndex
CREATE UNIQUE INDEX "database_backup_runs_job_id_key" ON "database_backup_runs"("job_id");

-- AddForeignKey
-- onDelete: SetNull, NOT Cascade — `job.history.purge` deletes `jobs` rows on
-- a retention schedule independent of the archive's own retention. Cascade
-- would let a routine job-history purge delete the record of a still-restorable
-- backup. See the block comment above `DatabaseBackupRun.jobId` in
-- prisma/schema.prisma.
ALTER TABLE "database_backup_runs" ADD CONSTRAINT "database_backup_runs_job_id_fkey" FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- =============================================================================
-- HAND-WRITTEN INDEX CHANGE — INTENTIONAL SCHEMA DRIFT (issue #351, epic #345)
-- =============================================================================
-- `database_backup_runs_active_uniq_idx` is NOT emitted by `prisma migrate
-- dev`/`diff` and never will be: Prisma's schema language has no syntax for a
-- partial index OR an expression index, so both the original (partial, on
-- `status`) and this tightened form (partial, on the expression `(true)`) are
-- written here by hand — exactly the drift `20260907120000_add_database_backup
-- _runs/migration.sql` already carries, now one step further. The block
-- comment above the `DatabaseBackupRun` model in prisma/schema.prisma records
-- the same fact for anyone reading the schema without this file open.
--
-- ⚠ `prisma migrate dev` WILL WANT TO DROP THIS INDEX on the next diff, the
-- same as its predecessor. Do not "reconcile" this migration by accepting
-- that drop, and do not expect `prisma migrate diff` against a live database
-- to come back clean. This drift is deliberate and permanent for as long as
-- Prisma's DSL lacks expression indexes.
-- =============================================================================

-- The ORIGINAL guard admitted at most one 'pending' row AND at most one
-- 'running' row AT THE SAME TIME — two active runs, not one — because the
-- index was keyed on the `status` column itself: a 'pending' row and a
-- 'running' row have different key values, so both could satisfy the same
-- unique index simultaneously. That was harmless only because nothing ever
-- wrote 'pending'. #351 makes the dump a queue job, and the run row is now
-- created 'pending' before any worker has claimed it, so 'pending' rows are
-- no longer hypothetical.
DROP INDEX "database_backup_runs_active_uniq_idx";

-- THE tightened single-active-run guard. Keying on the constant expression
-- `(true)` — rather than on `status` — makes every row matching the WHERE
-- predicate (any 'pending' or 'running' row) index to the exact same key, so
-- Postgres now enforces "at most one active row, full stop" across BOTH
-- statuses combined, not "at most one of each". A 'completed', 'failed' or
-- 'stale' row does not match the predicate at all and is never constrained by
-- this index, so any number of them may coexist.
--
-- `DatabaseBackupRunnerService.startBackup` still inserts optimistically and
-- turns the loser's P2002 into a typed `DatabaseBackupAlreadyRunningError`
-- carrying the winner's id; there is deliberately no `findFirst` pre-check,
-- because check-then-act is racy exactly when this constraint matters.
CREATE UNIQUE INDEX "database_backup_runs_active_uniq_idx"
  ON "database_backup_runs" ((true)) WHERE "status" IN ('pending','running');
