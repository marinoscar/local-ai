-- =============================================================================
-- Organization settings: one organization's overrides (PP-8.1, issue #733)
-- =============================================================================
--
-- One org-owned table, `org_settings`: one row per organization, holding only
-- the org-overridable fields of each system settings namespace that declares
-- an `org` block ({ "<namespace>": { "<field>": value } }), with its own
-- version for If-Match. NOT NULL org_id, ENABLE and FORCE ROW LEVEL SECURITY
-- and an org_settings_org_isolation policy copied from 0025_org_scoped_rls:
-- organization A's transaction never reads or writes organization B's row.
-- system_settings (deployment-level) and user_settings (user-owned) are
-- unchanged.
-- =============================================================================

-- CreateTable
CREATE TABLE "org_settings" (
    "id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "value" JSONB NOT NULL,
    "version" INTEGER NOT NULL DEFAULT 1,
    "updated_by_user_id" UUID,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "org_settings_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "org_settings_org_id_key" ON "org_settings"("org_id");

-- AddForeignKey
ALTER TABLE "org_settings" ADD CONSTRAINT "org_settings_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "org_settings" ADD CONSTRAINT "org_settings_updated_by_user_id_fkey" FOREIGN KEY ("updated_by_user_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- =============================================================================
-- Row-level security, as in 0025_org_scoped_rls. The policy name is listed in
-- RLS_POLICIES (rls-policies.json) and asserted against pg_policies by the
-- drift test. NULLIF(..., '') because current_setting(name, true) is the empty
-- string on a connection that ever held a transaction-local value.
-- =============================================================================

ALTER TABLE "org_settings" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "org_settings" FORCE ROW LEVEL SECURITY;
CREATE POLICY "org_settings_org_isolation" ON "org_settings"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');
