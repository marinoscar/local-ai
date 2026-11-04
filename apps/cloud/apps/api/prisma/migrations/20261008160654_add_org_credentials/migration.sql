-- =============================================================================
-- Organization credentials: the org tier of the credential store (PP-8.8, #735)
-- =============================================================================
--
-- One org-owned table, mirroring user_credentials with an organization as the
-- owner: org_credentials, addressed by (org_id, purpose, name), each secret
-- encrypted under the org-bound sub-key domain org:<orgId>:<purpose>.
--
-- A plain composite UNIQUE suffices (all three columns are NOT NULL), so no
-- raw-SQL index. The table is tenant data with a NOT NULL org_id and a FORCEd
-- org_credentials_org_isolation policy copied from 0025_org_scoped_rls.
--
-- No backfill: the table is new, so the policy is enabled at once.
-- =============================================================================

-- CreateTable
CREATE TABLE "org_credentials" (
    "id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "purpose" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "secret" TEXT NOT NULL,
    "hint" TEXT,
    "label" TEXT,
    "updated_by_user_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "org_credentials_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "org_credentials_org_id_purpose_name_key" ON "org_credentials"("org_id", "purpose", "name");

-- AddForeignKey
ALTER TABLE "org_credentials" ADD CONSTRAINT "org_credentials_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "org_credentials" ADD CONSTRAINT "org_credentials_updated_by_user_id_fkey" FOREIGN KEY ("updated_by_user_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- =============================================================================
-- Row-level security, as in 0025_org_scoped_rls. The policy name is listed in
-- RLS_POLICIES (rls-policies.json) and asserted against pg_policies by the
-- drift test. NULLIF(..., '') because current_setting(name, true) is the empty
-- string on a connection that ever held a transaction-local value.
-- =============================================================================

ALTER TABLE "org_credentials" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "org_credentials" FORCE ROW LEVEL SECURITY;
CREATE POLICY "org_credentials_org_isolation" ON "org_credentials"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');
