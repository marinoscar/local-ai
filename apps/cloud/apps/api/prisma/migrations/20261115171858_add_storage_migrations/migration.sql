-- =============================================================================
-- Copy-only storage provider migration: storage_migration_runs and
-- storage_migration_items (#1093)
-- =============================================================================
--
-- Two new org-owned tables. A run is deployment-wide today, so its org_id is
-- NULLABLE (NULL: visible to the bypass client only, as for a system-scope
-- webhook_endpoints row in 0039_add_webhooks). An item carries its object's
-- org_id (NOT NULL) because a policy cannot join through the parent, and links
-- to the object by the composite (object_id, org_id) foreign key (ADR 0002 D5).
--
-- storage_migration_runs_active_uniq_idx is a raw-SQL partial unique index
-- (listed in raw-sql-indexes.json): at most one pending or running run. No
-- backfill: the tables are new, so RLS is enabled at once.
-- =============================================================================

BEGIN;
SELECT set_config('app.rls_bypass', 'on', true);

-- CreateEnum
CREATE TYPE "StorageMigrationRunStatus" AS ENUM ('pending', 'running', 'completed', 'failed', 'cancelled');

-- CreateEnum
CREATE TYPE "StorageMigrationItemStatus" AS ENUM ('pending', 'copying', 'migrated', 'failed', 'skipped');

-- CreateTable
CREATE TABLE "storage_migration_runs" (
    "id" UUID NOT NULL,
    "org_id" UUID,
    "source_provider" VARCHAR(48) NOT NULL,
    "target_provider" VARCHAR(48) NOT NULL,
    "status" "StorageMigrationRunStatus" NOT NULL DEFAULT 'pending',
    "total_count" INTEGER NOT NULL DEFAULT 0,
    "migrated_count" INTEGER NOT NULL DEFAULT 0,
    "failed_count" INTEGER NOT NULL DEFAULT 0,
    "skipped_count" INTEGER NOT NULL DEFAULT 0,
    "planned_at" TIMESTAMPTZ,
    "started_by_id" UUID,
    "cancelled_by_id" UUID,
    "started_at" TIMESTAMPTZ,
    "finished_at" TIMESTAMPTZ,
    "cancelled_at" TIMESTAMPTZ,
    "last_error" VARCHAR(1000),
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "storage_migration_runs_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "storage_migration_items" (
    "id" UUID NOT NULL,
    "run_id" UUID NOT NULL,
    "object_id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "status" "StorageMigrationItemStatus" NOT NULL DEFAULT 'pending',
    "job_id" UUID,
    "size_bytes" BIGINT,
    "checksum_sha256" CHAR(64),
    "last_error" VARCHAR(1000),
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "storage_migration_items_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "storage_migration_runs_created_at_idx" ON "storage_migration_runs"("created_at" DESC);

-- CreateIndex
CREATE INDEX "storage_migration_runs_status_idx" ON "storage_migration_runs"("status");

-- CreateIndex
CREATE INDEX "storage_migration_runs_org_id_idx" ON "storage_migration_runs"("org_id");

-- CreateIndex
CREATE INDEX "storage_migration_items_run_id_status_idx" ON "storage_migration_items"("run_id", "status");

-- CreateIndex
CREATE INDEX "storage_migration_items_org_id_object_id_idx" ON "storage_migration_items"("org_id", "object_id");

-- CreateIndex
CREATE UNIQUE INDEX "storage_migration_items_run_id_object_id_key" ON "storage_migration_items"("run_id", "object_id");

-- AddForeignKey
ALTER TABLE "storage_migration_runs" ADD CONSTRAINT "storage_migration_runs_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "storage_migration_runs" ADD CONSTRAINT "storage_migration_runs_started_by_id_fkey" FOREIGN KEY ("started_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "storage_migration_runs" ADD CONSTRAINT "storage_migration_runs_cancelled_by_id_fkey" FOREIGN KEY ("cancelled_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "storage_migration_items" ADD CONSTRAINT "storage_migration_items_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "storage_migration_runs"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "storage_migration_items" ADD CONSTRAINT "storage_migration_items_object_id_org_id_fkey" FOREIGN KEY ("object_id", "org_id") REFERENCES "storage_objects"("id", "org_id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "storage_migration_items" ADD CONSTRAINT "storage_migration_items_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- At most one pending or running migration run, deployment-wide: a
-- constant-expression partial unique index Prisma cannot express. A concurrent
-- second start loses on this index (P2002 -> 409), never a findFirst pre-check.
CREATE UNIQUE INDEX "storage_migration_runs_active_uniq_idx" ON "storage_migration_runs" ((true)) WHERE "status" IN ('pending', 'running');

-- =============================================================================
-- Row-level security, as in 0039_add_webhooks. The policy names are listed in
-- RLS_POLICIES (rls-policies.json). A NULL org_id never equals app.org_id, so a
-- deployment-wide run needs the bypass.
-- =============================================================================

ALTER TABLE "storage_migration_runs" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "storage_migration_runs" FORCE ROW LEVEL SECURITY;
CREATE POLICY "storage_migration_runs_org_isolation" ON "storage_migration_runs"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

ALTER TABLE "storage_migration_items" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "storage_migration_items" FORCE ROW LEVEL SECURITY;
CREATE POLICY "storage_migration_items_org_isolation" ON "storage_migration_items"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

COMMIT;
