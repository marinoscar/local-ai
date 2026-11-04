-- =============================================================================
-- Per-feature AI usage (PP-14.17, #935)
-- =============================================================================
--
-- Persist the registered AI feature (`registerAiFeature`) a call was made for,
-- so usage can be filtered and grouped per feature from the database:
--
--   ai_runs.feature          the feature a background run was started for
--   ai_usage_events.feature  the feature a provider round trip was made for
--
-- Both columns are nullable plain TEXT: every row written before this
-- migration, a call made without a feature, and a catalogue sync have none.
-- No backfill, no default, no foreign key (a feature is code, not data, and a
-- row must outlive the feature that wrote it).
--
-- ai_runs and ai_usage_events are organization tables under forced row-level
-- security, so the migration runs with the bypass on for its transaction
-- (set_config with is_local = true: transaction-local, never session-level).
-- Adding a nullable column rewrites nothing; the index is built under the
-- table's SHARE lock, which blocks writes to ai_usage_events for the length of
-- the build (the table holds one row per provider round trip, an append-only
-- ledger a retention purge keeps bounded).
-- =============================================================================

BEGIN;
SELECT set_config('app.rls_bypass', 'on', true);

-- AlterTable
ALTER TABLE "ai_runs" ADD COLUMN     "feature" TEXT;

-- AlterTable
ALTER TABLE "ai_usage_events" ADD COLUMN     "feature" TEXT;

-- CreateIndex
CREATE INDEX "ai_usage_events_org_id_feature_created_at_idx" ON "ai_usage_events"("org_id", "feature", "created_at");

COMMIT;
