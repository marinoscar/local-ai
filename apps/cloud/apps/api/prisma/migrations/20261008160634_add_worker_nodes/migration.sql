-- =============================================================================
-- Worker Nodes (issue #267, epic #254)
-- =============================================================================
-- Adds `worker_nodes` and `node_credentials`, and wires the FK on
-- `jobs.claimed_by_node_id` that was deliberately left unconstrained by
-- 20260906120000_add_jobs (see that migration's own header comment, and the
-- block comment above the `Job` model and the `WorkerNode` model in
-- prisma/schema.prisma for the full reasoning behind every choice below —
-- this file intentionally does not restate it at length).
-- =============================================================================

-- CreateEnum
CREATE TYPE "NodeStatus" AS ENUM ('online', 'draining', 'offline', 'disabled');

-- CreateTable
CREATE TABLE "worker_nodes" (
    "id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "hostname" TEXT NOT NULL,
    "platform" TEXT NOT NULL,
    "cli_version" TEXT NOT NULL,
    "eligible_types" TEXT[],
    "concurrency" INTEGER NOT NULL,
    "status" "NodeStatus" NOT NULL DEFAULT 'offline',
    "capabilities" JSONB,
    "registered_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "last_heartbeat_at" TIMESTAMPTZ,
    "created_by_id" UUID NOT NULL,

    CONSTRAINT "worker_nodes_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "node_credentials" (
    "id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "token_hash" TEXT NOT NULL,
    "token_prefix" TEXT NOT NULL,
    "expires_at" TIMESTAMPTZ,
    "last_used_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "revoked_at" TIMESTAMPTZ,

    CONSTRAINT "node_credentials_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
-- The register-or-reattach anchor (#268's own words: "existing exactly this
-- way") — see the block comment above the `WorkerNode` model.
CREATE UNIQUE INDEX "worker_nodes_created_by_id_name_key" ON "worker_nodes"("created_by_id", "name");

-- CreateIndex
-- "Every online/draining/offline/disabled node" — the fleet page's primary
-- filter, and what #270's lifecycle cron scans.
CREATE INDEX "worker_nodes_status_idx" ON "worker_nodes"("status");

-- CreateIndex
-- "This user's own nodes" — backs `GET /nodes` and `assertOwnership` (#268).
CREATE INDEX "worker_nodes_created_by_id_idx" ON "worker_nodes"("created_by_id");

-- CreateIndex
CREATE UNIQUE INDEX "node_credentials_token_hash_key" ON "node_credentials"("token_hash");

-- CreateIndex
-- "This user's own credentials" — backs `GET /api/node-credentials`, the
-- same query shape as `personal_access_tokens_user_id_idx`.
CREATE INDEX "node_credentials_user_id_idx" ON "node_credentials"("user_id");

-- AddForeignKey
-- onDelete: Cascade — a WorkerNode is a live registration with no standalone
-- value once its owner is gone (its NodeCredential rows are already gone via
-- their own Cascade below, so an orphaned node could never authenticate
-- again). See the block comment above `WorkerNode.createdBy` in
-- prisma/schema.prisma for the full reasoning, including why this is NOT the
-- SetNull audit-trail pattern used elsewhere in this schema (AuditEvent,
-- Credential, NotificationDelivery).
ALTER TABLE "worker_nodes" ADD CONSTRAINT "worker_nodes_created_by_id_fkey" FOREIGN KEY ("created_by_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
-- onDelete: Cascade — mirrors `personal_access_tokens_user_id_fkey` exactly
-- (per issue #267): a node credential is a live authentication artifact with
-- no standalone value once its owning account is deleted.
ALTER TABLE "node_credentials" ADD CONSTRAINT "node_credentials_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
-- The FK deferred by 20260906120000_add_jobs. onDelete: SetNull — deleting a
-- WorkerNode must release the jobs it was holding, never delete job history.
-- See the "claimedByNodeId" block comment above the `Job` model.
ALTER TABLE "jobs" ADD CONSTRAINT "jobs_claimed_by_node_id_fkey" FOREIGN KEY ("claimed_by_node_id") REFERENCES "worker_nodes"("id") ON DELETE SET NULL ON UPDATE CASCADE;
