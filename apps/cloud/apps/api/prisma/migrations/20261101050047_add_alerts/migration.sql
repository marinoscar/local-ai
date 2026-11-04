-- CreateTable
CREATE TABLE "alert_rules" (
    "id" UUID NOT NULL,
    "org_id" UUID,
    "key" VARCHAR(64),
    "name" VARCHAR(120) NOT NULL,
    "description" VARCHAR(500),
    "enabled" BOOLEAN NOT NULL DEFAULT false,
    "kind" VARCHAR(32) NOT NULL,
    "severity" VARCHAR(16) NOT NULL,
    "config" JSONB NOT NULL,
    "for_seconds" INTEGER NOT NULL DEFAULT 0,
    "cooldown_seconds" INTEGER NOT NULL DEFAULT 900,
    "notify_channels" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "webhook" BOOLEAN NOT NULL DEFAULT true,
    "created_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,
    "last_evaluated_at" TIMESTAMPTZ,
    "last_error" VARCHAR(500),

    CONSTRAINT "alert_rules_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "alert_incidents" (
    "id" UUID NOT NULL,
    "rule_id" UUID NOT NULL,
    "labels_key" VARCHAR(512) NOT NULL,
    "labels" JSONB NOT NULL,
    "state" VARCHAR(16) NOT NULL,
    "severity" VARCHAR(16) NOT NULL,
    "pending_since" TIMESTAMPTZ,
    "fired_at" TIMESTAMPTZ,
    "resolved_at" TIMESTAMPTZ,
    "last_value" DOUBLE PRECISION,
    "last_seen_at" TIMESTAMPTZ NOT NULL,
    "summary" VARCHAR(500) NOT NULL,
    "acknowledged_at" TIMESTAMPTZ,
    "acknowledged_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "alert_incidents_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "alert_rules_key_key" ON "alert_rules"("key");

-- CreateIndex
CREATE INDEX "alert_rules_enabled_kind_idx" ON "alert_rules"("enabled", "kind");

-- CreateIndex
CREATE INDEX "alert_incidents_rule_id_state_idx" ON "alert_incidents"("rule_id", "state");

-- CreateIndex
CREATE INDEX "alert_incidents_state_fired_at_idx" ON "alert_incidents"("state", "fired_at");

-- CreateIndex
CREATE INDEX "alert_incidents_resolved_at_idx" ON "alert_incidents"("resolved_at");

-- AddForeignKey
ALTER TABLE "alert_incidents" ADD CONSTRAINT "alert_incidents_rule_id_fkey" FOREIGN KEY ("rule_id") REFERENCES "alert_rules"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- CreateIndex (raw SQL, intentional schema drift: Prisma cannot express a partial unique index)
-- One OPEN incident per rule and label set. The evaluator relies on it to stay idempotent.
CREATE UNIQUE INDEX "alert_incidents_open_uniq_idx" ON "alert_incidents"("rule_id", "labels_key") WHERE "state" IN ('pending', 'firing');
