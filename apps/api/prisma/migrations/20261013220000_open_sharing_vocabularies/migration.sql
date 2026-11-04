-- =============================================================================
-- Open the sharing vocabularies (PP-14.15b, issue #967)
-- =============================================================================
--
-- Two closed Prisma enums become plain TEXT, so an app can add a group role
-- (`registerGroupRole`) and a persisted grantee kind (`registerGranteeKind`)
-- at zero further migrations:
--
--   group_members.role, group_invites.role   "GroupRole"  admin | editor | viewer
--   grants.grantee_kind                      "GrantGranteeKind"  user | group | link
--
-- Every stored value is kept as it is (the cast is the enum's own label) and
-- both enums are dropped. No CHECK on the role columns on purpose: a row must
-- outlive the role that wrote it, and the allowed set is the API's registry.
--
-- grants gains the nullable `grantee_ref` TEXT: the reference of a grantee of an
-- app-registered kind (a domain, a team id), as the kind normalised it. The
-- built-in kinds keep their own columns (grantee_user_id, grantee_group_id,
-- link_token_hash) and leave it NULL.
--
-- RAW SQL (intentional schema drift, as in 0027_add_grants):
--   grants_grantee_consistency_check  relaxed: the three built-in row shapes
--                                     (now also with grantee_ref NULL) OR a row
--                                     of any OTHER kind that carries a
--                                     grantee_ref and none of the built-in
--                                     grantee columns. This CHECK is the second
--                                     line of defence behind the API.
--   grants_active_user_uniq_idx,
--   grants_active_group_uniq_idx      rebuilt unchanged apart from the predicate
--                                     literal, which was an enum cast and is
--                                     now text (an index predicate over a column
--                                     whose type changes cannot be re-parsed).
--   grants_active_ref_uniq_idx        NEW: at most one ACTIVE grant per
--                                     (resource, app kind, reference); the grants
--                                     API upserts on it.
-- All three indexes are listed in raw-sql-indexes.json.
--
-- group_members, group_invites and grants are organization tables under forced
-- row-level security, so the migration runs with the bypass on for its
-- transaction (set_config with is_local = true: transaction-local, never
-- session-level).
--
-- LOCKING. Each ALTER TABLE takes an ACCESS EXCLUSIVE lock held until COMMIT, so
-- no concurrent write can slip a duplicate in while an index is dropped and
-- rebuilt. The three column changes rewrite their tables (the sharing tables
-- are small: one row per membership, invite and grant).
-- =============================================================================

BEGIN;
SELECT set_config('app.rls_bypass', 'on', true);

-- Dependents of grants.grantee_kind: the two partial unique indexes and the CHECK.
DROP INDEX "grants_active_user_uniq_idx";
DROP INDEX "grants_active_group_uniq_idx";
ALTER TABLE "grants" DROP CONSTRAINT "grants_grantee_consistency_check";

-- AlterTable (the default is an enum literal: drop it first, set the text one after)
ALTER TABLE "group_members" ALTER COLUMN "role" DROP DEFAULT;
ALTER TABLE "group_members" ALTER COLUMN "role" TYPE TEXT USING "role"::text;
ALTER TABLE "group_members" ALTER COLUMN "role" SET DEFAULT 'viewer';

-- AlterTable
ALTER TABLE "group_invites" ALTER COLUMN "role" DROP DEFAULT;
ALTER TABLE "group_invites" ALTER COLUMN "role" TYPE TEXT USING "role"::text;
ALTER TABLE "group_invites" ALTER COLUMN "role" SET DEFAULT 'viewer';

-- AlterTable
ALTER TABLE "grants" ALTER COLUMN "grantee_kind" TYPE TEXT USING "grantee_kind"::text;
ALTER TABLE "grants" ADD COLUMN "grantee_ref" TEXT;

-- DropEnum
DROP TYPE "GroupRole";

-- DropEnum
DROP TYPE "GrantGranteeKind";

-- CreateIndex (partial: one active grant per resource and user)
CREATE UNIQUE INDEX "grants_active_user_uniq_idx" ON "grants"("resource_type", "resource_id", "grantee_user_id") WHERE "grantee_kind" = 'user' AND "revoked_at" IS NULL;

-- CreateIndex (partial: one active grant per resource and group)
CREATE UNIQUE INDEX "grants_active_group_uniq_idx" ON "grants"("resource_type", "resource_id", "grantee_group_id") WHERE "grantee_kind" = 'group' AND "revoked_at" IS NULL;

-- CreateIndex (partial: one active grant per resource, app-registered kind and reference)
CREATE UNIQUE INDEX "grants_active_ref_uniq_idx" ON "grants"("resource_type", "resource_id", "grantee_kind", "grantee_ref") WHERE "grantee_kind" NOT IN ('user', 'group', 'link') AND "revoked_at" IS NULL;

-- AddCheckConstraint (the grantee columns match the grantee kind)
ALTER TABLE "grants" ADD CONSTRAINT "grants_grantee_consistency_check" CHECK (
  ("grantee_kind" = 'user'  AND "grantee_user_id" IS NOT NULL AND "grantee_group_id" IS NULL     AND "link_token_hash" IS NULL     AND "grantee_ref" IS NULL)
  OR
  ("grantee_kind" = 'group' AND "grantee_user_id" IS NULL     AND "grantee_group_id" IS NOT NULL AND "link_token_hash" IS NULL     AND "grantee_ref" IS NULL)
  OR
  ("grantee_kind" = 'link'  AND "grantee_user_id" IS NULL     AND "grantee_group_id" IS NULL     AND "link_token_hash" IS NOT NULL AND "grantee_ref" IS NULL)
  OR
  ("grantee_kind" NOT IN ('user', 'group', 'link') AND "grantee_ref" IS NOT NULL AND "grantee_user_id" IS NULL AND "grantee_group_id" IS NULL AND "link_token_hash" IS NULL)
);

COMMIT;
