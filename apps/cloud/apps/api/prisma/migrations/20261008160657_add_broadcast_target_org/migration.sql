-- Broadcasts can target one organization (issue #738, PP-8.5).
--
-- Nullable with no default: a metadata-only ADD COLUMN. NULL is a system
-- broadcast (every active user of the deployment), and every row written
-- before this migration is one. No row-level security on
-- `notification_broadcasts`: a system broadcast belongs to no organization and
-- the delivering job must read it; the API authorizes `target_org_id` instead.
-- ON DELETE CASCADE: an organization's broadcasts have no audience once it is
-- deleted.

-- AlterTable
ALTER TABLE "notification_broadcasts" ADD COLUMN     "target_org_id" UUID;

-- CreateIndex
CREATE INDEX "notification_broadcasts_target_org_id_idx" ON "notification_broadcasts"("target_org_id");

-- AddForeignKey
-- NOT VALID then VALIDATE, as for `jobs.org_id`: the add takes its lock
-- without scanning the table, and the validation scan does not block writes.
ALTER TABLE "notification_broadcasts" ADD CONSTRAINT "notification_broadcasts_target_org_id_fkey" FOREIGN KEY ("target_org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE NOT VALID;
ALTER TABLE "notification_broadcasts" VALIDATE CONSTRAINT "notification_broadcasts_target_org_id_fkey";
