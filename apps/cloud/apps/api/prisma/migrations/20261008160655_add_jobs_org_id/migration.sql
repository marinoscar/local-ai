-- Jobs carry the organization they belong to (issue #734, PP-8.2).
--
-- Nullable with no default: a metadata-only ADD COLUMN, no table rewrite.
-- NULL is a deployment-wide (system) job, and every row written before this
-- migration is one. No row-level security on `jobs` (the claim is one
-- cross-organization statement); see the `Job.orgId` comment in the jobs
-- fragment. The `(org_id, status)` index is built CONCURRENTLY in the next
-- migration, on its own, because `jobs` is a big table.

-- AlterTable
ALTER TABLE "jobs" ADD COLUMN     "org_id" UUID;

-- AddForeignKey
-- NOT VALID then VALIDATE: the add takes its lock without scanning `jobs`,
-- and the validation scan runs under a lock that does not block writes.
-- ON DELETE SET NULL keeps an organization's job history when it is deleted.
ALTER TABLE "jobs" ADD CONSTRAINT "jobs_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE SET NULL ON UPDATE CASCADE NOT VALID;
ALTER TABLE "jobs" VALIDATE CONSTRAINT "jobs_org_id_fkey";
