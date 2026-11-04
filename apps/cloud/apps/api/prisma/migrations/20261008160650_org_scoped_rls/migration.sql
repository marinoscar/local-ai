-- =============================================================================
-- org_id on tenant-scoped tables and row-level security (PP-6.5, issue #725)
-- =============================================================================
--
-- The database enforces organization isolation (ADR 0002 D5). Four tables are
-- org-owned and get a NOT NULL (or, for usage events, nullable) org_id plus a
-- FORCEd policy keyed on transaction-local settings; audit_events gets a
-- nullable org_id and no policy in this release.
--
--   storage_objects        org_id NOT NULL, FK RESTRICT, RLS forced
--   storage_object_chunks  org_id NOT NULL, FK RESTRICT, RLS forced; its link to
--                          the parent is now COMPOSITE (object_id, org_id) ->
--                          storage_objects (id, org_id): a foreign-key check
--                          bypasses row-level security, so a plain key would let
--                          one organization store a reference to another's row
--   ai_runs                org_id NOT NULL, FK RESTRICT, RLS forced. Restrict, not
--                          SetNull: the column is NOT NULL, so history cannot
--                          survive an organization's deletion by nulling it
--   ai_usage_events        org_id nullable, FK SET NULL, RLS forced. Nullable so
--                          a deployment-wide event (a catalogue sync) and the
--                          history of a deleted organization survive; such a row
--                          is visible to the system (bypass) client only
--   audit_events           org_id nullable, FK SET NULL, NO row-level security
--
-- BACKFILL. Every existing row of the three NOT NULL tables, and of
-- ai_usage_events, goes to the default organization (created by
-- 0023_add_organizations on every database). Two exceptions keep NULL:
--
--   - ai_usage_events with operation = 'catalog': a catalogue sync is run by the
--     deployment, not by an organization, and new ones are written without one.
--   - audit_events whose action is a SYSTEM action. Everything else (including
--     an action this migration does not know) belongs to the default
--     organization. System action prefixes:
--         system_settings:  email_settings:  push_config:  storage_config:
--         ai_config:  ai_model:  ai_catalog:  telemetry:  support_bundle:
--         maintenance:  db_restore:  db_backup:  allowlist:  users:  nodes:
--         jobs.  notification_broadcast.  settings.  retention.
--
-- The backfill runs in an explicit transaction that raises the bypass flag
-- (the pattern ADR 0002 prescribes for any migration that touches an RLS
-- table: under FORCE a bare UPDATE would silently change zero rows). The
-- policies are enabled AFTER it, so on a first apply the flag is belt and
-- braces; it keeps the pattern in front of the next reader.
--
-- THE APPLICATION ROLE MUST BE NOSUPERUSER NOBYPASSRLS, or every policy below is
-- inert (a superuser sees every row of a FORCEd table). The block at the end
-- warns when the role applying this migration is not an ordinary role. The
-- Doctor check `db.rls_role` fails while any FORCEd table exists and the
-- application role is a superuser or has BYPASSRLS.
--
-- DEPLOYMENT NOTE. The statements below take short locks and build the new
-- indexes inside the migration transaction; on a very large storage_objects or
-- ai_usage_events table pre-create the indexes with CREATE INDEX CONCURRENTLY
-- IF NOT EXISTS under the same names before applying.
-- =============================================================================

-- DropForeignKey
ALTER TABLE "storage_object_chunks" DROP CONSTRAINT "storage_object_chunks_object_id_fkey";

-- AlterTable (expand: nullable first, so existing rows can be backfilled)
ALTER TABLE "storage_objects" ADD COLUMN     "org_id" UUID;
ALTER TABLE "storage_object_chunks" ADD COLUMN     "org_id" UUID;
ALTER TABLE "ai_runs" ADD COLUMN     "org_id" UUID;
ALTER TABLE "ai_usage_events" ADD COLUMN     "org_id" UUID;
ALTER TABLE "audit_events" ADD COLUMN     "org_id" UUID;

-- Backfill, as system work.
BEGIN;
SELECT set_config('app.rls_bypass', 'on', true);

UPDATE "storage_objects"
   SET "org_id" = (SELECT "id" FROM "organizations" WHERE "is_default")
 WHERE "org_id" IS NULL;

UPDATE "storage_object_chunks" AS c
   SET "org_id" = o."org_id"
  FROM "storage_objects" AS o
 WHERE c."object_id" = o."id" AND c."org_id" IS NULL;

UPDATE "ai_runs"
   SET "org_id" = (SELECT "id" FROM "organizations" WHERE "is_default")
 WHERE "org_id" IS NULL;

UPDATE "ai_usage_events"
   SET "org_id" = (SELECT "id" FROM "organizations" WHERE "is_default")
 WHERE "org_id" IS NULL AND "operation" <> 'catalog';

UPDATE "audit_events"
   SET "org_id" = (SELECT "id" FROM "organizations" WHERE "is_default")
 WHERE "org_id" IS NULL
   AND "action" NOT LIKE 'system\_settings:%'
   AND "action" NOT LIKE 'email\_settings:%'
   AND "action" NOT LIKE 'push\_config:%'
   AND "action" NOT LIKE 'storage\_config:%'
   AND "action" NOT LIKE 'ai\_config:%'
   AND "action" NOT LIKE 'ai\_model:%'
   AND "action" NOT LIKE 'ai\_catalog:%'
   AND "action" NOT LIKE 'telemetry:%'
   AND "action" NOT LIKE 'support\_bundle:%'
   AND "action" NOT LIKE 'maintenance:%'
   AND "action" NOT LIKE 'db\_restore:%'
   AND "action" NOT LIKE 'db\_backup:%'
   AND "action" NOT LIKE 'allowlist:%'
   AND "action" NOT LIKE 'users:%'
   AND "action" NOT LIKE 'nodes:%'
   AND "action" NOT LIKE 'jobs.%'
   AND "action" NOT LIKE 'notification\_broadcast.%'
   AND "action" NOT LIKE 'settings.%'
   AND "action" NOT LIKE 'retention.%';
COMMIT;

-- AlterTable (contract: the three tables whose column is NOT NULL)
ALTER TABLE "storage_objects" ALTER COLUMN "org_id" SET NOT NULL;
ALTER TABLE "storage_object_chunks" ALTER COLUMN "org_id" SET NOT NULL;
ALTER TABLE "ai_runs" ALTER COLUMN "org_id" SET NOT NULL;

-- CreateIndex
CREATE UNIQUE INDEX "storage_objects_id_org_id_key" ON "storage_objects"("id", "org_id");

-- CreateIndex
CREATE INDEX "storage_objects_org_id_uploaded_by_id_created_at_idx" ON "storage_objects"("org_id", "uploaded_by_id", "created_at");

-- CreateIndex
CREATE INDEX "storage_objects_org_id_status_idx" ON "storage_objects"("org_id", "status");

-- CreateIndex
CREATE INDEX "storage_object_chunks_org_id_object_id_idx" ON "storage_object_chunks"("org_id", "object_id");

-- CreateIndex
CREATE INDEX "ai_runs_org_id_user_id_created_at_idx" ON "ai_runs"("org_id", "user_id", "created_at");

-- CreateIndex
CREATE INDEX "ai_usage_events_org_id_user_id_created_at_idx" ON "ai_usage_events"("org_id", "user_id", "created_at");

-- CreateIndex
CREATE INDEX "ai_usage_events_org_id_created_at_idx" ON "ai_usage_events"("org_id", "created_at");

-- CreateIndex
CREATE INDEX "audit_events_org_id_created_at_idx" ON "audit_events"("org_id", "created_at");

-- AddForeignKey
ALTER TABLE "storage_objects" ADD CONSTRAINT "storage_objects_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey (composite: object_id and org_id together)
ALTER TABLE "storage_object_chunks" ADD CONSTRAINT "storage_object_chunks_object_id_org_id_fkey" FOREIGN KEY ("object_id", "org_id") REFERENCES "storage_objects"("id", "org_id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "storage_object_chunks" ADD CONSTRAINT "storage_object_chunks_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ai_runs" ADD CONSTRAINT "ai_runs_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ai_usage_events" ADD CONSTRAINT "ai_usage_events_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "audit_events" ADD CONSTRAINT "audit_events_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- =============================================================================
-- Row-level security. Prisma's schema language cannot express any of this and
-- its diff ignores it; the policy names are listed in RLS_POLICIES
-- (packages/platform-db/src/drift/rls-policies.ts) and asserted against
-- pg_policies by the drift test.
--
-- NULLIF(..., '') is required: current_setting(name, true) is NULL on a fresh
-- connection but the EMPTY STRING on a connection that has ever held a
-- transaction-local value, and a bare ''::uuid cast errors.
-- FORCE applies the policy to the table owner, which is the application role:
-- unscoped code sees no rows and cannot insert (it fails closed).
-- =============================================================================

ALTER TABLE "storage_objects" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "storage_objects" FORCE ROW LEVEL SECURITY;
CREATE POLICY "storage_objects_org_isolation" ON "storage_objects"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

ALTER TABLE "storage_object_chunks" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "storage_object_chunks" FORCE ROW LEVEL SECURITY;
CREATE POLICY "storage_object_chunks_org_isolation" ON "storage_object_chunks"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

ALTER TABLE "ai_runs" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "ai_runs" FORCE ROW LEVEL SECURITY;
CREATE POLICY "ai_runs_org_isolation" ON "ai_runs"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

ALTER TABLE "ai_usage_events" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "ai_usage_events" FORCE ROW LEVEL SECURITY;
CREATE POLICY "ai_usage_events_org_isolation" ON "ai_usage_events"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

-- A policy is inert for a superuser or a BYPASSRLS role. Warn (do not fail:
-- the migration role may legitimately differ from the application role).
DO $$
DECLARE
  inert boolean;
BEGIN
  SELECT rolsuper OR rolbypassrls INTO inert FROM pg_roles WHERE rolname = current_user;
  IF inert THEN
    RAISE WARNING 'row-level security is INERT for role "%": it is a superuser or has BYPASSRLS. Run the application as an ordinary role (NOSUPERUSER NOBYPASSRLS) or tenant isolation is not enforced.', current_user;
  END IF;
END
$$;
