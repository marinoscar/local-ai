-- =============================================================================
-- Split RBAC into system roles and org roles (PP-6.3, issue #723)
-- =============================================================================
--
-- System roles operate the deployment and stay in "user_roles" (admin). Org
-- roles operate one organization and move onto the membership
-- ("memberships"."role_id"): org_admin, contributor, viewer. Every role and
-- permission gains a "scope". Effective permissions become the system roles'
-- grants plus the grants of the current organization's membership role, so the
-- data below is moved so that every user keeps exactly what they had.
--
-- Forward-only and in one transaction: the expand (columns) and the data move
-- happen together, so no request between "migrate deploy" and the seed run
-- sees an administrator without a permission. Nothing is dropped: "user_roles"
-- keeps its shape (expand/contract); only rows pointing at org roles go.
-- Every INSERT names its id (0005_drop_stale_uuid_defaults).
-- =============================================================================

-- CreateEnum
CREATE TYPE "RoleScope" AS ENUM ('system', 'org');

-- AlterTable: nullable first, backfilled below, then NOT NULL.
ALTER TABLE "memberships" ADD COLUMN     "role_id" UUID;

-- AlterTable: NULL means the default org role (viewer).
ALTER TABLE "org_invites" ADD COLUMN     "role_id" UUID;

-- AlterTable
ALTER TABLE "permissions" ADD COLUMN     "scope" "RoleScope" NOT NULL DEFAULT 'system';

-- AlterTable
ALTER TABLE "roles" ADD COLUMN     "scope" "RoleScope" NOT NULL DEFAULT 'system';

-- -----------------------------------------------------------------------------
-- 1. The org roles exist. org_admin is new; viewer is the fallback role of
--    step 4 and may be missing on a database the seed has not reached yet.
--    The seed refreshes both descriptions.
-- -----------------------------------------------------------------------------
INSERT INTO "roles" ("id", "name", "description", "scope")
VALUES (gen_random_uuid(), 'org_admin', 'Organization administrator - everything a contributor can do, plus manage the organization members and invites', 'org')
ON CONFLICT ("name") DO NOTHING;

INSERT INTO "roles" ("id", "name", "description", "scope")
VALUES (gen_random_uuid(), 'viewer', 'Read-only organization member - view content and manage own settings', 'org')
ON CONFLICT ("name") DO NOTHING;

-- -----------------------------------------------------------------------------
-- 2. Scopes. admin stays 'system' (the column default), like every
--    operational permission. An app's own roles and permissions keep the
--    default until the seed writes the scope their declarations name.
-- -----------------------------------------------------------------------------
UPDATE "roles" SET "scope" = 'org' WHERE "name" IN ('org_admin', 'contributor', 'viewer');

UPDATE "permissions" SET "scope" = 'org'
WHERE "name" IN (
  'user_settings:read', 'user_settings:write', 'storage:read', 'storage:write', 'ai:use',
  'org_members:read', 'org_members:write', 'org_invites:read', 'org_invites:write'
);

-- -----------------------------------------------------------------------------
-- 3. admin's grants of org permissions move to org_admin: copy first, then
--    delete. Every admin gets org_admin on their membership in step 4, in this
--    same transaction, so no administrator loses a permission. Grants an
--    administrator made to contributor or viewer are left as they are.
-- -----------------------------------------------------------------------------
INSERT INTO "role_permissions" ("role_id", "permission_id")
SELECT oa."id", rp."permission_id"
FROM "role_permissions" rp
JOIN "roles" a ON a."id" = rp."role_id" AND a."name" = 'admin'
JOIN "permissions" p ON p."id" = rp."permission_id" AND p."scope" = 'org'
CROSS JOIN "roles" oa
WHERE oa."name" = 'org_admin'
ON CONFLICT DO NOTHING;

DELETE FROM "role_permissions" rp
USING "roles" a, "permissions" p
WHERE rp."role_id" = a."id" AND a."name" = 'admin'
  AND rp."permission_id" = p."id" AND p."scope" = 'org';

-- -----------------------------------------------------------------------------
-- 4. Every user is a member of the default organization (0023 did this for
--    the users that existed then; this catches any created since through a
--    path that skipped it), and every membership gets its role from the
--    user's global roles: admin -> org_admin; otherwise the highest of
--    contributor > viewer; none -> viewer (the default org role).
-- -----------------------------------------------------------------------------
INSERT INTO "memberships" ("id", "org_id", "user_id", "status", "last_active_at", "created_at", "updated_at")
SELECT gen_random_uuid(), o."id", u."id", 'active', now(), now(), now()
FROM "users" u
CROSS JOIN "organizations" o
WHERE o."is_default"
ON CONFLICT ("org_id", "user_id") DO NOTHING;

UPDATE "memberships" m
SET "role_id" = (
  SELECT r."id" FROM "roles" r
  WHERE r."name" = CASE
    WHEN EXISTS (
      SELECT 1 FROM "user_roles" ur JOIN "roles" x ON x."id" = ur."role_id"
      WHERE ur."user_id" = m."user_id" AND x."name" = 'admin'
    ) THEN 'org_admin'
    WHEN EXISTS (
      SELECT 1 FROM "user_roles" ur JOIN "roles" x ON x."id" = ur."role_id"
      WHERE ur."user_id" = m."user_id" AND x."name" = 'contributor'
    ) THEN 'contributor'
    ELSE 'viewer'
  END
)
WHERE m."role_id" IS NULL;

ALTER TABLE "memberships" ALTER COLUMN "role_id" SET NOT NULL;

-- -----------------------------------------------------------------------------
-- 5. Contract, in data only: the global assignments of org roles are now
--    carried by the memberships. admin rows (system) stay.
-- -----------------------------------------------------------------------------
DELETE FROM "user_roles" ur
USING "roles" r
WHERE ur."role_id" = r."id" AND r."scope" = 'org';

-- CreateIndex
CREATE INDEX "memberships_role_id_idx" ON "memberships"("role_id");

-- CreateIndex
CREATE INDEX "org_invites_role_id_idx" ON "org_invites"("role_id");

-- AddForeignKey
ALTER TABLE "memberships" ADD CONSTRAINT "memberships_role_id_fkey" FOREIGN KEY ("role_id") REFERENCES "roles"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "org_invites" ADD CONSTRAINT "org_invites_role_id_fkey" FOREIGN KEY ("role_id") REFERENCES "roles"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
