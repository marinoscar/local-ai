-- CreateTable
CREATE TABLE "ai_models" (
    "id" UUID NOT NULL,
    "provider" TEXT NOT NULL,
    "model_id" TEXT NOT NULL,
    "display_name" TEXT,
    "capabilities" JSONB NOT NULL,
    "capability_source" TEXT NOT NULL DEFAULT 'unclassified',
    "enabled" BOOLEAN NOT NULL DEFAULT false,
    "context_window" INTEGER,
    "max_output_tokens" INTEGER,
    "discovered_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "last_seen_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "deprecated_at" TIMESTAMPTZ,
    "updated_by_user_id" UUID,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "ai_models_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "user_ai_keys" (
    "id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "provider" TEXT NOT NULL,
    "secret" TEXT NOT NULL,
    "hint" TEXT,
    "verified_at" TIMESTAMPTZ,
    "last_error_code" TEXT,
    "reachable_model_ids" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "reachable_checked_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "user_ai_keys_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ai_runs" (
    "id" UUID NOT NULL,
    "user_id" UUID,
    "job_id" UUID,
    "status" TEXT NOT NULL DEFAULT 'pending',
    "provider" TEXT NOT NULL,
    "model_id" TEXT NOT NULL,
    "request" JSONB NOT NULL,
    "output" JSONB,
    "error_code" TEXT,
    "error_message" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,
    "completed_at" TIMESTAMPTZ,

    CONSTRAINT "ai_runs_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ai_usage_events" (
    "id" UUID NOT NULL,
    "user_id" UUID,
    "provider" TEXT NOT NULL,
    "model_id" TEXT NOT NULL,
    "operation" TEXT NOT NULL,
    "key_source" TEXT NOT NULL,
    "input_tokens" INTEGER,
    "output_tokens" INTEGER,
    "reasoning_tokens" INTEGER,
    "cached_input_tokens" INTEGER,
    "units" JSONB,
    "latency_ms" INTEGER NOT NULL,
    "status" TEXT NOT NULL,
    "error_code" TEXT,
    "provider_request_id" TEXT,
    "job_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "ai_usage_events_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "ai_models_provider_enabled_idx" ON "ai_models"("provider", "enabled");

-- CreateIndex
CREATE UNIQUE INDEX "ai_models_provider_model_id_key" ON "ai_models"("provider", "model_id");

-- CreateIndex
CREATE UNIQUE INDEX "user_ai_keys_user_id_provider_key" ON "user_ai_keys"("user_id", "provider");

-- CreateIndex
CREATE INDEX "ai_runs_user_id_created_at_idx" ON "ai_runs"("user_id", "created_at");

-- CreateIndex
CREATE INDEX "ai_usage_events_user_id_created_at_idx" ON "ai_usage_events"("user_id", "created_at");

-- CreateIndex
CREATE INDEX "ai_usage_events_created_at_idx" ON "ai_usage_events"("created_at");

-- AddForeignKey
ALTER TABLE "ai_models" ADD CONSTRAINT "ai_models_updated_by_user_id_fkey" FOREIGN KEY ("updated_by_user_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "user_ai_keys" ADD CONSTRAINT "user_ai_keys_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ai_runs" ADD CONSTRAINT "ai_runs_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ai_usage_events" ADD CONSTRAINT "ai_usage_events_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;
