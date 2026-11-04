-- =============================================================================
-- Groups: sharing groups inside an organization (PP-7.1, issue #728)
-- =============================================================================
--
-- Three org-owned tables, each with a NOT NULL org_id and a FORCEd
-- <table>_org_isolation policy copied from 0025_org_scoped_rls:
--
--   groups          a set of users inside ONE organization that can own content
--                   (spec decision D6: a group is never a tenant)
--   group_members   one row per (group, user), with a GroupRole
--   group_invites   an invitation of an e-mail address to a group
--
-- ORG CONSISTENCY. A member and an invite reach their group through the
-- COMPOSITE key (group_id, org_id) -> groups (id, org_id), with the supporting
-- UNIQUE (id, org_id) on groups: a foreign-key check bypasses row-level
-- security, so a plain key would let one organization store a reference to
-- another's group, and the composite key also guarantees that
-- group_members.org_id and group_invites.org_id equal groups.org_id.
--
-- RAW-SQL INDEX (intentional schema drift, listed in raw-sql-indexes.json):
--   group_invites_pending_uniq_idx  at most one PENDING invite per (group,
--                                   email); a declined, revoked or accepted
--                                   invite does not block a new one. Prisma
--                                   cannot express a partial unique index.
--
-- No backfill: the tables are new, so the policies are enabled at once.
-- =============================================================================

-- CreateEnum
CREATE TYPE "GroupRole" AS ENUM ('admin', 'editor', 'viewer');

-- CreateTable
CREATE TABLE "groups" (
    "id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "description" TEXT,
    "metadata" JSONB,
    "created_by_id" UUID,
    "version" INTEGER NOT NULL DEFAULT 1,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "groups_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "group_members" (
    "id" UUID NOT NULL,
    "group_id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "role" "GroupRole" NOT NULL DEFAULT 'viewer',
    "added_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "group_members_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "group_invites" (
    "id" UUID NOT NULL,
    "group_id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "email" TEXT NOT NULL,
    "role" "GroupRole" NOT NULL DEFAULT 'viewer',
    "invited_by_id" UUID,
    "expires_at" TIMESTAMPTZ,
    "accepted_at" TIMESTAMPTZ,
    "accepted_by_id" UUID,
    "declined_at" TIMESTAMPTZ,
    "revoked_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "group_invites_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "groups_org_id_idx" ON "groups"("org_id");

-- CreateIndex
CREATE UNIQUE INDEX "groups_id_org_id_key" ON "groups"("id", "org_id");

-- CreateIndex
CREATE INDEX "group_members_user_id_idx" ON "group_members"("user_id");

-- CreateIndex
CREATE INDEX "group_members_org_id_idx" ON "group_members"("org_id");

-- CreateIndex
CREATE UNIQUE INDEX "group_members_group_id_user_id_key" ON "group_members"("group_id", "user_id");

-- CreateIndex
CREATE INDEX "group_invites_email_idx" ON "group_invites"("email");

-- CreateIndex
CREATE INDEX "group_invites_group_id_idx" ON "group_invites"("group_id");

-- CreateIndex
CREATE INDEX "group_invites_org_id_idx" ON "group_invites"("org_id");

-- AddForeignKey
ALTER TABLE "groups" ADD CONSTRAINT "groups_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "groups" ADD CONSTRAINT "groups_created_by_id_fkey" FOREIGN KEY ("created_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_members" ADD CONSTRAINT "group_members_group_id_org_id_fkey" FOREIGN KEY ("group_id", "org_id") REFERENCES "groups"("id", "org_id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_members" ADD CONSTRAINT "group_members_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_members" ADD CONSTRAINT "group_members_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_members" ADD CONSTRAINT "group_members_added_by_id_fkey" FOREIGN KEY ("added_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_invites" ADD CONSTRAINT "group_invites_group_id_org_id_fkey" FOREIGN KEY ("group_id", "org_id") REFERENCES "groups"("id", "org_id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_invites" ADD CONSTRAINT "group_invites_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_invites" ADD CONSTRAINT "group_invites_invited_by_id_fkey" FOREIGN KEY ("invited_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "group_invites" ADD CONSTRAINT "group_invites_accepted_by_id_fkey" FOREIGN KEY ("accepted_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- CreateIndex (partial: one pending invite per group and address)
CREATE UNIQUE INDEX "group_invites_pending_uniq_idx" ON "group_invites"("group_id", "email") WHERE "accepted_at" IS NULL AND "declined_at" IS NULL AND "revoked_at" IS NULL;

-- =============================================================================
-- Row-level security, as in 0025_org_scoped_rls. The policy names are listed in
-- RLS_POLICIES (rls-policies.json) and asserted against pg_policies by the
-- drift test. NULLIF(..., '') because current_setting(name, true) is the empty
-- string on a connection that ever held a transaction-local value.
-- =============================================================================

ALTER TABLE "groups" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "groups" FORCE ROW LEVEL SECURITY;
CREATE POLICY "groups_org_isolation" ON "groups"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

ALTER TABLE "group_members" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "group_members" FORCE ROW LEVEL SECURITY;
CREATE POLICY "group_members_org_isolation" ON "group_members"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

ALTER TABLE "group_invites" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "group_invites" FORCE ROW LEVEL SECURITY;
CREATE POLICY "group_invites_org_isolation" ON "group_invites"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');
