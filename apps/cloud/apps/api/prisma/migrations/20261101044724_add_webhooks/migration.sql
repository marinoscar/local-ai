-- =============================================================================
-- Outbound webhooks: webhook_endpoints and webhook_deliveries (#1046)
-- =============================================================================
--
-- Two new org-owned tables. org_id is NULLABLE on both: NULL only for a
-- system-scope endpoint and its deliveries, visible to the bypass (system)
-- client only, as for ai_usage_events in 0025_org_scoped_rls. A delivery carries
-- its endpoint's org_id because a policy cannot join through its parent; the
-- endpoint link is a simple FK (a composite FK on a nullable org_id is not
-- enforced by MATCH SIMPLE and would add no isolation). The signing secret is
-- never a column: it lives in the encrypted credentials store.
--
-- No raw-SQL partial index: the (endpoint_id, event_id) uniqueness is a plain
-- composite UNIQUE. No backfill: the tables are new, so RLS is enabled at once.
-- =============================================================================

BEGIN;
SELECT set_config('app.rls_bypass', 'on', true);

-- CreateEnum
CREATE TYPE "WebhookScope" AS ENUM ('user', 'org', 'system');

-- CreateEnum
CREATE TYPE "WebhookDeliveryStatus" AS ENUM ('pending', 'delivering', 'succeeded', 'failed', 'dead');

-- CreateTable
CREATE TABLE "webhook_endpoints" (
    "id" UUID NOT NULL,
    "org_id" UUID,
    "scope" "WebhookScope" NOT NULL,
    "owner_user_id" UUID,
    "created_by_id" UUID,
    "name" VARCHAR(120) NOT NULL,
    "description" VARCHAR(500),
    "url" VARCHAR(2048) NOT NULL,
    "event_keys" TEXT[],
    "enabled" BOOLEAN NOT NULL DEFAULT true,
    "secret_version" INTEGER NOT NULL DEFAULT 1,
    "previous_secret_until" TIMESTAMPTZ,
    "consecutive_failures" INTEGER NOT NULL DEFAULT 0,
    "disabled_reason" VARCHAR(64),
    "last_delivery_at" TIMESTAMPTZ,
    "last_success_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "webhook_endpoints_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "webhook_deliveries" (
    "id" UUID NOT NULL,
    "org_id" UUID,
    "endpoint_id" UUID NOT NULL,
    "event_key" VARCHAR(128) NOT NULL,
    "event_id" UUID NOT NULL,
    "payload" JSONB NOT NULL,
    "status" "WebhookDeliveryStatus" NOT NULL DEFAULT 'pending',
    "attempt" INTEGER NOT NULL DEFAULT 0,
    "response_status" INTEGER,
    "response_snippet" VARCHAR(1024),
    "error_code" VARCHAR(64),
    "duration_ms" INTEGER,
    "next_attempt_at" TIMESTAMPTZ,
    "delivered_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "webhook_deliveries_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "webhook_endpoints_org_id_scope_enabled_idx" ON "webhook_endpoints"("org_id", "scope", "enabled");

-- CreateIndex
CREATE INDEX "webhook_endpoints_owner_user_id_idx" ON "webhook_endpoints"("owner_user_id");

-- CreateIndex
CREATE INDEX "webhook_deliveries_endpoint_id_created_at_idx" ON "webhook_deliveries"("endpoint_id", "created_at" DESC);

-- CreateIndex
CREATE INDEX "webhook_deliveries_org_id_created_at_idx" ON "webhook_deliveries"("org_id", "created_at");

-- CreateIndex
CREATE INDEX "webhook_deliveries_event_id_idx" ON "webhook_deliveries"("event_id");

-- CreateIndex
CREATE INDEX "webhook_deliveries_created_at_idx" ON "webhook_deliveries"("created_at");

-- CreateIndex
CREATE UNIQUE INDEX "webhook_deliveries_endpoint_id_event_id_key" ON "webhook_deliveries"("endpoint_id", "event_id");

-- AddForeignKey
ALTER TABLE "webhook_endpoints" ADD CONSTRAINT "webhook_endpoints_owner_user_id_fkey" FOREIGN KEY ("owner_user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "webhook_endpoints" ADD CONSTRAINT "webhook_endpoints_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "webhook_deliveries" ADD CONSTRAINT "webhook_deliveries_endpoint_id_fkey" FOREIGN KEY ("endpoint_id") REFERENCES "webhook_endpoints"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- =============================================================================
-- Row-level security, as in 0025_org_scoped_rls (ai_usage_events). The policy
-- names are listed in RLS_POLICIES (rls-policies.json) and asserted against
-- pg_policies by the drift test. NULLIF(..., '') because current_setting(name,
-- true) is the empty string on a connection that ever held a transaction-local
-- value. A NULL org_id never equals app.org_id, so those rows need the bypass.
-- =============================================================================

ALTER TABLE "webhook_endpoints" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "webhook_endpoints" FORCE ROW LEVEL SECURITY;
CREATE POLICY "webhook_endpoints_org_isolation" ON "webhook_endpoints"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

ALTER TABLE "webhook_deliveries" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "webhook_deliveries" FORCE ROW LEVEL SECURITY;
CREATE POLICY "webhook_deliveries_org_isolation" ON "webhook_deliveries"
  USING      ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on')
  WITH CHECK ("org_id" = NULLIF(current_setting('app.org_id', true), '')::uuid
              OR current_setting('app.rls_bypass', true) = 'on');

COMMIT;
