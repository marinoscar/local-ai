-- CreateEnum
CREATE TYPE "JobStatus" AS ENUM ('pending', 'running', 'succeeded', 'failed');

-- CreateEnum
CREATE TYPE "JobReason" AS ENUM ('upload', 'rerun', 'backfill');

-- CreateTable
CREATE TABLE "jobs" (
    "id" UUID NOT NULL,
    "type" TEXT NOT NULL,
    "subject_type" TEXT,
    "subject_id" TEXT,
    "dedup_key" TEXT,
    "status" "JobStatus" NOT NULL DEFAULT 'pending',
    "reason" "JobReason" NOT NULL,
    "priority" INTEGER NOT NULL DEFAULT 0,
    "provider_key" TEXT,
    "model_version" TEXT,
    "payload" JSONB,
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "last_error" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "started_at" TIMESTAMPTZ,
    "finished_at" TIMESTAMPTZ,
    "scheduled_for" TIMESTAMPTZ,
    "rate_limited_at" TIMESTAMPTZ,
    "rate_limit_hits" INTEGER NOT NULL DEFAULT 0,
    "claimed_by_node_id" UUID,
    "lease_expires_at" TIMESTAMPTZ,
    "executor" TEXT,

    CONSTRAINT "jobs_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "job_stats_rollup" (
    "type" TEXT NOT NULL,
    "succeeded_count" INTEGER NOT NULL DEFAULT 0,
    "failed_count" INTEGER NOT NULL DEFAULT 0,
    "sum_duration_ms" DOUBLE PRECISION NOT NULL DEFAULT 0,
    "duration_samples" INTEGER NOT NULL DEFAULT 0,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "job_stats_rollup_pkey" PRIMARY KEY ("type")
);

-- CreateIndex
CREATE INDEX "jobs_status_priority_created_at_idx" ON "jobs"("status", "priority", "created_at");

-- CreateIndex
CREATE INDEX "jobs_status_scheduled_for_priority_created_at_idx" ON "jobs"("status", "scheduled_for", "priority", "created_at");

-- CreateIndex
CREATE INDEX "jobs_subject_type_subject_id_idx" ON "jobs"("subject_type", "subject_id");

-- CreateIndex
CREATE INDEX "jobs_type_status_idx" ON "jobs"("type", "status");

-- CreateIndex
CREATE INDEX "jobs_status_lease_expires_at_idx" ON "jobs"("status", "lease_expires_at");

-- CreateIndex
CREATE INDEX "jobs_claimed_by_node_id_idx" ON "jobs"("claimed_by_node_id");

-- CreateIndex
CREATE INDEX "jobs_created_at_idx" ON "jobs"("created_at" DESC);

-- CreateIndex
-- Covering index for the two unconditional admin groupBy({ by: ['status'] })-
-- shaped counts: status, type and id are the only columns those queries read,
-- so Postgres can answer them as an index-only scan without visiting the heap.
CREATE INDEX "jobs_status_type_id_idx" ON "jobs"("status", "type", "id");

-- =============================================================================
-- HAND-WRITTEN INDEXES — INTENTIONAL SCHEMA DRIFT (issue #255, epic #254)
-- =============================================================================
-- The three indexes below are NOT emitted by `prisma migrate dev`/`diff` and
-- never will be: each needs a partial-index `WHERE` clause, which the Prisma
-- schema language has no syntax for. They are written here by hand, and the
-- block comment above the `Job` model in prisma/schema.prisma records the
-- same fact for anyone reading the schema without this file open. Do not
-- "reconcile" this migration by dropping them, and do not expect `prisma
-- migrate diff` against the live database to come back clean — this drift is
-- deliberate and permanent for as long as Prisma's DSL lacks partial indexes.
-- =============================================================================

-- Enforces the active-dedup constraint described at length above the `Job`
-- model in prisma/schema.prisma ("dedupKey / the active-dedup unique index").
-- The key format itself is defined once in buildDedupKey()
-- (apps/api/src/jobs/job-keys.ts) — this index does not care how the string
-- was built, only that it is unique among jobs that are still active.
-- `dedup_key IS NOT NULL` lets jobs that opt out of dedup (a NULL key) insert
-- freely; the status filter is what makes a key reusable once its job
-- finishes (succeeded or failed jobs drop out of this index's predicate).
CREATE UNIQUE INDEX "jobs_active_dedup_uniq_idx"
  ON "jobs"("dedup_key") WHERE "status" IN ('pending','running') AND "dedup_key" IS NOT NULL;

-- "Show me flaky jobs" without indexing the (large) majority that succeeded
-- on their first attempt. See "attempts vs rateLimitedAt/rateLimitHits" above
-- the `Job` model for why `attempts` and rate-limit deferrals are counted
-- separately in the first place.
CREATE INDEX "jobs_attempts_gt1_idx" ON "jobs"("attempts") WHERE "attempts" > 1;

-- Feeds the per-type duration stats that `JobStatsRollup` accumulates:
-- only jobs that actually reached `succeeded` with both timestamps recorded
-- have a meaningful duration, so the index is scoped to exactly those rows
-- rather than the full table.
CREATE INDEX "jobs_succeeded_duration_idx"
  ON "jobs"("finished_at","started_at","type")
  WHERE "status"='succeeded' AND "started_at" IS NOT NULL AND "finished_at" IS NOT NULL;
