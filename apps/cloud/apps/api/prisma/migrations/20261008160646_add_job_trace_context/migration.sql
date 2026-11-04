-- Issue #607: carry the enqueuing request's W3C trace context on the job row.
-- Nullable, no default, no index: existing rows simply have no trace context.
-- The partial unique index jobs_active_dedup_uniq_idx is deliberately untouched.

-- AlterTable
ALTER TABLE "jobs" ADD COLUMN "trace_context" TEXT;
