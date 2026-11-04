-- add_localai (CLOUD-04, docs/specs/cloud-app.md section 3.5): the Local AI tables, three raw-SQL partial
-- unique indexes, and the removal of the starter's `notes` and `documents` tables (the composed schema no
-- longer has the models; the installed add_notes and add_documents migrations stay untouched).
-- Runs as system work: `documents` is a row-level-security table (transaction-local bypass, never a session SET).
BEGIN;
SELECT set_config('app.rls_bypass', 'on', true);

/*
  Warnings:

  - You are about to drop the `documents` table. If the table is not empty, all the data it contains will be lost.
  - You are about to drop the `notes` table. If the table is not empty, all the data it contains will be lost.

*/
-- CreateEnum
CREATE TYPE "LocalaiNodePhase" AS ENUM ('pending', 'enrolled', 'retired');

-- CreateEnum
CREATE TYPE "LocalaiServePattern" AS ENUM ('A', 'B', 'none');

-- CreateEnum
CREATE TYPE "LocalaiModelStatus" AS ENUM ('loaded', 'loading', 'unloaded', 'failed');

-- CreateEnum
CREATE TYPE "LocalaiCatalogStatus" AS ENUM ('draft', 'invalid', 'valid', 'approved', 'superseded');

-- CreateEnum
CREATE TYPE "LocalaiCatalogSource" AS ENUM ('github_release', 'upload', 'editor');

-- CreateEnum
CREATE TYPE "LocalaiCommandStatus" AS ENUM ('queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled');

-- CreateEnum
CREATE TYPE "LocalaiAlertState" AS ENUM ('active', 'cleared');

-- DropForeignKey
ALTER TABLE "documents" DROP CONSTRAINT "documents_org_id_fkey";

-- DropForeignKey
ALTER TABLE "documents" DROP CONSTRAINT "documents_owner_user_id_fkey";

-- DropForeignKey
ALTER TABLE "notes" DROP CONSTRAINT "notes_user_id_fkey";

-- DropTable
DROP TABLE "documents";

-- DropTable
DROP TABLE "notes";

-- CreateTable
CREATE TABLE "localai_nodes" (
    "id" UUID NOT NULL,
    "node_id" TEXT NOT NULL,
    "display_name" TEXT NOT NULL,
    "hardware" TEXT,
    "backend" TEXT,
    "public_host" TEXT NOT NULL,
    "serve_pattern" "LocalaiServePattern" NOT NULL,
    "tailnet_machine" TEXT,
    "tailnet_ip" TEXT,
    "profile" TEXT,
    "mode" TEXT,
    "connectivity" TEXT,
    "agent_version" TEXT,
    "catalog_version" TEXT,
    "catalog_pin" TEXT,
    "gateway_enabled" BOOLEAN NOT NULL DEFAULT true,
    "gateway_priority" INTEGER NOT NULL DEFAULT 100,
    "phase" "LocalaiNodePhase" NOT NULL DEFAULT 'pending',
    "worker_node_id" UUID,
    "node_credential_id" UUID,
    "last_state" JSONB,
    "last_state_at" TIMESTAMPTZ,
    "last_state_seq" BIGINT,
    "key_hint" TEXT,
    "key_reported_at" TIMESTAMPTZ,
    "publish_checklist" JSONB NOT NULL DEFAULT '{}',
    "created_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,
    "retired_at" TIMESTAMPTZ,

    CONSTRAINT "localai_nodes_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "localai_node_models" (
    "node_id" UUID NOT NULL,
    "model_id" TEXT NOT NULL,
    "present" BOOLEAN NOT NULL,
    "verified" BOOLEAN NOT NULL,
    "loadable" BOOLEAN NOT NULL,
    "loadable_reason" TEXT,
    "status" "LocalaiModelStatus" NOT NULL,
    "is_default" BOOLEAN NOT NULL DEFAULT false,
    "slots" INTEGER NOT NULL DEFAULT 1,
    "roles" TEXT[],
    "input_modalities" TEXT[],
    "ctx" INTEGER,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "localai_node_models_pkey" PRIMARY KEY ("node_id","model_id")
);

-- CreateTable
CREATE TABLE "localai_catalog_versions" (
    "id" UUID NOT NULL,
    "version" TEXT NOT NULL,
    "source" "LocalaiCatalogSource" NOT NULL,
    "source_ref" TEXT,
    "status" "LocalaiCatalogStatus" NOT NULL DEFAULT 'draft',
    "files" JSONB NOT NULL,
    "bundle_sha256" TEXT NOT NULL,
    "lint_result" JSONB,
    "linted_at" TIMESTAMPTZ,
    "notes" TEXT,
    "created_by_id" UUID,
    "approved_by_id" UUID,
    "approved_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "localai_catalog_versions_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "localai_commands" (
    "id" UUID NOT NULL,
    "node_id" UUID NOT NULL,
    "type" TEXT NOT NULL,
    "params" JSONB NOT NULL,
    "status" "LocalaiCommandStatus" NOT NULL DEFAULT 'queued',
    "dedup_key" TEXT,
    "source" TEXT NOT NULL DEFAULT 'ui',
    "requested_by_id" UUID,
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "claim_token" UUID,
    "lease_expires_at" TIMESTAMPTZ,
    "claimed_at" TIMESTAMPTZ,
    "expires_at" TIMESTAMPTZ NOT NULL,
    "finished_at" TIMESTAMPTZ,
    "result" JSONB,
    "error" JSONB,
    "traceparent" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "localai_commands_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "localai_alerts" (
    "id" UUID NOT NULL,
    "node_id" UUID,
    "kind" TEXT NOT NULL,
    "code" TEXT NOT NULL,
    "severity" TEXT NOT NULL,
    "state" "LocalaiAlertState" NOT NULL DEFAULT 'active',
    "title" TEXT NOT NULL,
    "details" JSONB,
    "first_raised_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "last_seen_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "cleared_at" TIMESTAMPTZ,
    "acknowledged_at" TIMESTAMPTZ,
    "acknowledged_by_id" UUID,
    "notified_at" TIMESTAMPTZ,

    CONSTRAINT "localai_alerts_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "localai_nodes_node_id_key" ON "localai_nodes"("node_id");

-- CreateIndex
CREATE UNIQUE INDEX "localai_nodes_worker_node_id_key" ON "localai_nodes"("worker_node_id");

-- CreateIndex
CREATE UNIQUE INDEX "localai_nodes_node_credential_id_key" ON "localai_nodes"("node_credential_id");

-- CreateIndex
CREATE INDEX "localai_node_models_model_id_status_idx" ON "localai_node_models"("model_id", "status");

-- CreateIndex
CREATE UNIQUE INDEX "localai_catalog_versions_version_key" ON "localai_catalog_versions"("version");

-- CreateIndex
CREATE INDEX "localai_catalog_versions_status_idx" ON "localai_catalog_versions"("status");

-- CreateIndex
CREATE INDEX "localai_commands_node_id_status_created_at_idx" ON "localai_commands"("node_id", "status", "created_at");

-- CreateIndex
CREATE INDEX "localai_commands_status_expires_at_idx" ON "localai_commands"("status", "expires_at");

-- CreateIndex
CREATE INDEX "localai_alerts_state_severity_idx" ON "localai_alerts"("state", "severity");

-- AddForeignKey
ALTER TABLE "localai_nodes" ADD CONSTRAINT "localai_nodes_created_by_id_fkey" FOREIGN KEY ("created_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_node_models" ADD CONSTRAINT "localai_node_models_node_id_fkey" FOREIGN KEY ("node_id") REFERENCES "localai_nodes"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_catalog_versions" ADD CONSTRAINT "localai_catalog_versions_created_by_id_fkey" FOREIGN KEY ("created_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_catalog_versions" ADD CONSTRAINT "localai_catalog_versions_approved_by_id_fkey" FOREIGN KEY ("approved_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_commands" ADD CONSTRAINT "localai_commands_node_id_fkey" FOREIGN KEY ("node_id") REFERENCES "localai_nodes"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_commands" ADD CONSTRAINT "localai_commands_requested_by_id_fkey" FOREIGN KEY ("requested_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_alerts" ADD CONSTRAINT "localai_alerts_node_id_fkey" FOREIGN KEY ("node_id") REFERENCES "localai_nodes"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- ---------------------------------------------------------------------------
-- Raw-SQL partial unique indexes (Prisma cannot express them; intentional schema drift, listed in
-- prisma/platform.lock `rawSqlIndexes`). Never replace them with @@unique or a findFirst pre-check.
-- ---------------------------------------------------------------------------

-- At most one approved catalog version: every row maps to the single key `true` while approved.
CREATE UNIQUE INDEX "localai_catalog_one_approved" ON "localai_catalog_versions" ((true)) WHERE "status" = 'approved';

-- At most one queued or running command per (node, dedup key); a null dedup key never conflicts.
CREATE UNIQUE INDEX "localai_commands_active_dedup" ON "localai_commands" ("node_id", "dedup_key")
  WHERE "status" IN ('queued', 'running') AND "dedup_key" IS NOT NULL;

-- At most one active alert per (node, kind); a lab-wide alert (node_id null) is keyed on the all-zero uuid.
CREATE UNIQUE INDEX "localai_alerts_one_active" ON "localai_alerts"
  (coalesce("node_id", '00000000-0000-0000-0000-000000000000'::uuid), "kind") WHERE "state" = 'active';

COMMIT;
