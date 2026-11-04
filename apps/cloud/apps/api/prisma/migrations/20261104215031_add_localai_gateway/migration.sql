-- CreateEnum
CREATE TYPE "LocalaiGatewayKeyKind" AS ENUM ('user', 'service');

-- CreateTable
CREATE TABLE "localai_gateway_keys" (
    "id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "kind" "LocalaiGatewayKeyKind" NOT NULL DEFAULT 'user',
    "token_hash" TEXT NOT NULL,
    "token_prefix" TEXT NOT NULL,
    "allowed_models" TEXT[],
    "rpm_limit" INTEGER NOT NULL,
    "concurrent_limit" INTEGER NOT NULL,
    "daily_token_budget" BIGINT,
    "expires_at" TIMESTAMPTZ,
    "revoked_at" TIMESTAMPTZ,
    "last_used_at" TIMESTAMPTZ,
    "created_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "localai_gateway_keys_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "localai_gateway_usage" (
    "id" UUID NOT NULL,
    "key_id" UUID,
    "user_id" UUID,
    "request_id" TEXT NOT NULL,
    "endpoint" TEXT NOT NULL,
    "model" TEXT,
    "requested_model" TEXT,
    "node_id" TEXT,
    "status" INTEGER NOT NULL,
    "error_code" TEXT,
    "stream" BOOLEAN NOT NULL,
    "aborted" BOOLEAN NOT NULL DEFAULT false,
    "attempts" INTEGER NOT NULL DEFAULT 1,
    "prompt_tokens" INTEGER,
    "completion_tokens" INTEGER,
    "usage_source" TEXT NOT NULL,
    "ttfb_ms" INTEGER,
    "duration_ms" INTEGER NOT NULL,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "localai_gateway_usage_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "localai_gateway_usage_daily" (
    "key_id" UUID NOT NULL,
    "day" DATE NOT NULL,
    "requests" INTEGER NOT NULL DEFAULT 0,
    "prompt_tokens" BIGINT NOT NULL DEFAULT 0,
    "completion_tokens" BIGINT NOT NULL DEFAULT 0,

    CONSTRAINT "localai_gateway_usage_daily_pkey" PRIMARY KEY ("key_id","day")
);

-- CreateTable
CREATE TABLE "localai_model_policies" (
    "model_id" TEXT NOT NULL,
    "enabled" BOOLEAN NOT NULL DEFAULT true,
    "aliases" TEXT[],
    "on_demand_load" BOOLEAN NOT NULL DEFAULT false,
    "load_wait_s" INTEGER NOT NULL DEFAULT 120,
    "first_byte_timeout_s" INTEGER,
    "preferred_nodes" TEXT[],
    "updated_by_id" UUID,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "localai_model_policies_pkey" PRIMARY KEY ("model_id")
);

-- CreateIndex
CREATE UNIQUE INDEX "localai_gateway_keys_token_hash_key" ON "localai_gateway_keys"("token_hash");

-- CreateIndex
CREATE INDEX "localai_gateway_keys_user_id_idx" ON "localai_gateway_keys"("user_id");

-- CreateIndex
CREATE INDEX "localai_gateway_usage_key_id_created_at_idx" ON "localai_gateway_usage"("key_id", "created_at");

-- CreateIndex
CREATE INDEX "localai_gateway_usage_created_at_idx" ON "localai_gateway_usage"("created_at");

-- AddForeignKey
ALTER TABLE "localai_gateway_keys" ADD CONSTRAINT "localai_gateway_keys_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_gateway_usage" ADD CONSTRAINT "localai_gateway_usage_key_id_fkey" FOREIGN KEY ("key_id") REFERENCES "localai_gateway_keys"("id") ON DELETE SET NULL ON UPDATE CASCADE;
