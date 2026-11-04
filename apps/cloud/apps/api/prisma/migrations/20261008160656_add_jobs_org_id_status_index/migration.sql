-- "This organization's jobs by status" (issue #734, PP-8.2): the admin job
-- list's orgId filter and offboarding's cancel of an organization's pending
-- jobs. CONCURRENTLY, so building it never blocks the queue's writes, and
-- therefore the only statement in this migration (it cannot run inside a
-- transaction, and Prisma runs a single such statement outside one).
CREATE INDEX CONCURRENTLY IF NOT EXISTS "jobs_org_id_status_idx" ON "jobs"("org_id", "status");
