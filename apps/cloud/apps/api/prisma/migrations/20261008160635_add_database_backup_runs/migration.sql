-- CreateEnum
CREATE TYPE "DatabaseBackupStatus" AS ENUM ('pending', 'running', 'completed', 'failed', 'stale');

-- CreateEnum
CREATE TYPE "DatabaseBackupTrigger" AS ENUM ('manual', 'scheduled', 'pre_restore');

-- CreateTable
CREATE TABLE "database_backup_runs" (
    "id" UUID NOT NULL,
    "status" "DatabaseBackupStatus" NOT NULL DEFAULT 'pending',
    "trigger" "DatabaseBackupTrigger" NOT NULL,
    "started_at" TIMESTAMPTZ,
    "finished_at" TIMESTAMPTZ,
    "last_heartbeat_at" TIMESTAMPTZ,
    "bytes_written" BIGINT NOT NULL DEFAULT 0,
    "size_bytes" BIGINT NOT NULL DEFAULT 0,
    "storage_provider" TEXT NOT NULL,
    "storage_key" TEXT NOT NULL,
    "bucket" TEXT NOT NULL,
    "format" TEXT NOT NULL,
    "checksum_sha256" TEXT,
    "db_version" TEXT,
    "app_version" TEXT,
    "migration_name" TEXT,
    "verified_at" TIMESTAMPTZ,
    "last_error" TEXT,
    "created_by_id" UUID,
    "restore_status" TEXT,
    "restore_error" TEXT,
    "restored_at" TIMESTAMPTZ,
    "restored_by_id" UUID,
    "restore_scratch_db" TEXT,
    "restore_old_db" TEXT,
    "swapped_at" TIMESTAMPTZ,
    "pre_restore_backup_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "database_backup_runs_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "database_backup_runs_created_at_idx" ON "database_backup_runs"("created_at" DESC);

-- CreateIndex
CREATE INDEX "database_backup_runs_started_at_idx" ON "database_backup_runs"("started_at" DESC);

-- CreateIndex
CREATE INDEX "database_backup_runs_status_idx" ON "database_backup_runs"("status");

-- CreateIndex
CREATE INDEX "database_backup_runs_status_created_at_idx" ON "database_backup_runs"("status", "created_at" DESC);

-- AddForeignKey
ALTER TABLE "database_backup_runs" ADD CONSTRAINT "database_backup_runs_created_by_id_fkey" FOREIGN KEY ("created_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "database_backup_runs" ADD CONSTRAINT "database_backup_runs_restored_by_id_fkey" FOREIGN KEY ("restored_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "database_backup_runs" ADD CONSTRAINT "database_backup_runs_pre_restore_backup_id_fkey" FOREIGN KEY ("pre_restore_backup_id") REFERENCES "database_backup_runs"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- =============================================================================
-- HAND-WRITTEN INDEX — INTENTIONAL SCHEMA DRIFT (issue #281, epic #254)
-- =============================================================================
-- The index below is NOT emitted by `prisma migrate dev`/`diff` and never will
-- be: it needs a partial-index `WHERE` clause, which the Prisma schema language
-- has no syntax for. It is written here by hand, and the block comment above
-- the `DatabaseBackupRun` model in prisma/schema.prisma records the same fact
-- for anyone reading the schema without this file open.
--
-- ⚠ `prisma migrate dev` WILL WANT TO DROP IT on the next diff, exactly as it
-- would for the three partial indexes in 20260906120000_add_jobs. Do not
-- "reconcile" this migration by accepting that drop, and do not expect
-- `prisma migrate diff` against a live database to come back clean. This drift
-- is deliberate and permanent for as long as Prisma's DSL lacks partial
-- indexes. If a future `migrate dev` run emits a `DROP INDEX
-- "database_backup_runs_active_uniq_idx"`, delete that line from the generated
-- migration before committing it.
-- =============================================================================

-- THE single-active-run guard. It is what makes "at most one backup at a time"
-- true ACROSS REPLICAS, rather than true only within one process: a scheduled
-- tick on one container and an administrator's click on another cannot both
-- insert, so two `pg_dump` processes can never stream into the derived storage
-- key at once. `DatabaseBackupRunnerService.startBackup` inserts optimistically
-- and turns the loser's P2002 into a typed
-- `DatabaseBackupAlreadyRunningError` carrying the winner's id; there is
-- deliberately no `findFirst` pre-check anywhere in that path, because
-- check-then-act is racy exactly when this constraint matters.
--
-- No cast is needed on the literals: Postgres coerces an unadorned string
-- literal to the column's enum type in an `IN` list, the same way
-- "jobs_active_dedup_uniq_idx" does for "JobStatus".
--
-- ⚠ WHAT THIS ADMITS, PRECISELY: a UNIQUE index on ("status") filtered to two
-- values permits at most one 'pending' row AND at most one 'running' row. The
-- runner only ever inserts 'running', so today the ceiling is exactly one
-- active run; 'pending' is covered so that a future path which does insert one
-- is still arbitrated here rather than in application code. A future design
-- that genuinely needs both states populated at once must TIGHTEN this index
-- (to a constant expression) — never relax the guard into a service.
CREATE UNIQUE INDEX "database_backup_runs_active_uniq_idx"
  ON "database_backup_runs" ("status") WHERE "status" IN ('pending','running');
