-- =============================================================================
-- Grants: share one record with a user, a group or a link (PP-7.2, issue #729)
-- =============================================================================
--
-- One org-owned table, `grants`, with a NOT NULL org_id and a FORCEd
-- grants_org_isolation policy copied from 0025_org_scoped_rls.
--
-- POLYMORPHIC RESOURCE. (resource_type, resource_id) names a row of an app
-- table the platform cannot know, so there is no foreign key to it. Integrity
-- comes from GrantsService.deleteForResources() (inside the app's delete
-- transaction) and the sharing.grants.prune job.
--
-- ORG CONSISTENCY. A group grantee reaches its group through the COMPOSITE key
-- (grantee_group_id, org_id) -> groups (id, org_id): a foreign-key check
-- bypasses row-level security, so a plain key would let a grant name another
-- organization's group. A user is not org-scoped; the API checks that the
-- grantee is an active member of the resource's organization.
--
-- The link columns (link_token_hash, link_token_ciphertext, link_label) are
-- created now so #730 needs no migration.
--
-- RAW SQL (intentional schema drift: Prisma can express neither):
--   grants_active_user_uniq_idx       at most one ACTIVE (unrevoked) grant per
--                                     (resource, user): the API upserts on it
--   grants_active_group_uniq_idx      the same per (resource, group)
--   grants_grantee_consistency_check  the grantee columns match grantee_kind
-- The two indexes are listed in raw-sql-indexes.json.
--
-- No backfill: the table is new, so the policy is enabled at once.
-- =============================================================================

-- CreateEnum
CREATE TYPE "GrantGranteeKind" AS ENUM ('user', 'group', 'link');

-- CreateTable
CREATE TABLE "grants" (
    "id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "resource_type" TEXT NOT NULL,
    "resource_id" UUID NOT NULL,
    "grantee_kind" "GrantGranteeKind" NOT NULL,
    "grantee_user_id" UUID,
    "grantee_group_id" UUID,
    "role" TEXT NOT NULL,
    "link_token_hash" TEXT,
    "link_token_ciphertext" TEXT,
    "link_label" TEXT,
    "expires_at" TIMESTAMPTZ,
    "revoked_at" TIMESTAMPTZ,
    "revoked_by_id" UUID,
    "granted_by_id" UUID,
    "metadata" JSONB,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "grants_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "grants_link_token_hash_key" ON "grants"("link_token_hash");

-- CreateIndex
CREATE INDEX "grants_resource_type_resource_id_idx" ON "grants"("resource_type", "resource_id");

-- CreateIndex
CREATE INDEX "grants_grantee_user_id_idx" ON "grants"("grantee_user_id");

-- CreateIndex
CREATE INDEX "grants_grantee_group_id_idx" ON "grants"("grantee_group_id");

-- CreateIndex
CREATE INDEX "grants_org_id_idx" ON "grants"("org_id");

-- AddForeignKey
ALTER TABLE "grants" ADD CONSTRAINT "grants_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "grants" ADD CONSTRAINT "grants_grantee_user_id_fkey" FOREIGN KEY ("grantee_user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "grants" ADD CONSTRAINT "grants_grantee_group_id_org_id_fkey" FOREIGN KEY ("grantee_group_id", "org_id") REFERENCES "groups"("id", "org_id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "grants" ADD CONSTRAINT "grants_revoked_by_id_fkey" FOREIGN KEY ("revoked_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "grants" ADD CONSTRAINT "grants_granted_by_id_fkey" FOREIGN KEY ("granted_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- CreateIndex (partial: one active grant per resource and user)
CREATE UNIQUE INDEX "grants_active_user_uniq_idx" ON "grants"("resource_type", "resource_id", "grantee_user_id") WHERE "grantee_kind" = 'user' AND "revoked_at" IS NULL;

-- CreateIndex (partial: one active grant per resource and group)
CREATE UNIQUE INDEX "grants_active_group_uniq_idx" ON "grants"("resource_type", "resource_id", "grantee_group_id") WHERE "grantee_kind" = 'group' AND "revoked_at" IS NULL;

-- AddCheckConstraint (the grantee columns match the grantee kind)
ALTER TABLE "grants" ADD CONSTRAINT "grants_grantee_consistency_check" CHECK (
  ("grantee_kind" = 'user'  AND "grantee_user_id" IS NOT NULL AND "grantee_group_id" IS NULL     AND "link_token_hash" IS NULL)
  OR
  ("grantee_kind" = 'group' AND "grantee_user_id" IS NULL     AND "grantee_group_id" IS NOT NULL AND "link_token_hash" IS NULL)
  OR
  ("grantee_kind" = 'link'  AND "grantee_user_id" IS NULL     AND "grantee_group_id" IS NULL     AND "link_token_hash" IS NOT NULL)
);

-- =============================================================================
-- Row-level security, as in 0025_org_scoped_rls. The policy name is listed in
-- RLS_POLICIES (rls-policies.json) and asserted against pg_policies by the
-- drift test. NULLIF(..., '') because current_setting(name, true) is the empty
-- string on a connection that ever held a transaction-local value.
-- =============================================================================

ALTER TABLE "grants" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "grants" FORCE ROW LEVEL SECURITY;
CREATE POLICY "grants_org_isolation" ON "grants"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');
