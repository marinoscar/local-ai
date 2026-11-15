-- AlterTable
ALTER TABLE "notifications" ADD COLUMN     "data" JSONB,
ADD COLUMN     "dedup_key" TEXT,
ADD COLUMN     "dismissed_at" TIMESTAMPTZ,
ADD COLUMN     "scope_id" UUID,
ADD COLUMN     "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- CreateIndex
CREATE INDEX "notifications_dismissed_at_idx" ON "notifications"("dismissed_at");

-- Existing rows were last changed when they were created (or read); the new
-- column's default would otherwise stamp them all with the migration time.
UPDATE "notifications" SET "updated_at" = COALESCE("read_at", "created_at");

-- At most one LIVE foldable (STATE or COUNTED) row per (user, dedup key).
-- Raw SQL: Prisma cannot express a partial unique index. Listed in
-- packages/platform-db/raw-sql-indexes.json; never replace it with @@unique or
-- a find-then-insert.
CREATE UNIQUE INDEX "notifications_live_dedup_uniq_idx" ON "notifications"("user_id", "dedup_key") WHERE "dismissed_at" IS NULL AND "dedup_key" IS NOT NULL;
