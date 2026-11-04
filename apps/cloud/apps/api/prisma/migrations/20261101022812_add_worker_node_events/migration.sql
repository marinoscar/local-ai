-- CreateTable
CREATE TABLE "worker_node_events" (
    "id" UUID NOT NULL,
    "node_id" UUID NOT NULL,
    "type" VARCHAR(64) NOT NULL,
    "severity" VARCHAR(16) NOT NULL,
    "attrs" JSONB,
    "occurred_at" TIMESTAMPTZ NOT NULL,
    "received_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "worker_node_events_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "worker_node_events_node_id_received_at_idx" ON "worker_node_events"("node_id", "received_at" DESC);

-- CreateIndex
CREATE INDEX "worker_node_events_received_at_idx" ON "worker_node_events"("received_at");

-- AddForeignKey
ALTER TABLE "worker_node_events" ADD CONSTRAINT "worker_node_events_node_id_fkey" FOREIGN KEY ("node_id") REFERENCES "worker_nodes"("id") ON DELETE CASCADE ON UPDATE CASCADE;
