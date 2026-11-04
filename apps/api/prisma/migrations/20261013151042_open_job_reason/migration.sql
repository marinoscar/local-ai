-- Open the job-reason vocabulary: "jobs"."reason" goes from the closed
-- "JobReason" enum ('upload', 'rerun', 'backfill') to plain TEXT, so an app can
-- register its own reasons (`registerJobReason`) at zero migrations. Every
-- stored value is kept as it is: the cast is the enum's own label.
--
-- No CHECK constraint on purpose: a row must outlive the reason that wrote it,
-- and the allowed shape is the contract's `jobReasonSchema` pattern at the API
-- edge. "jobs" has no row-level security, so no bypass is needed here.
--
-- The column type changes in place (a table rewrite, taken under an ACCESS
-- EXCLUSIVE lock for its duration); no index covers "reason", so nothing is
-- rebuilt besides the heap. The enum type is dropped last: nothing else uses it.
ALTER TABLE "jobs" ALTER COLUMN "reason" TYPE TEXT USING "reason"::text;

-- DropEnum
DROP TYPE "JobReason";
