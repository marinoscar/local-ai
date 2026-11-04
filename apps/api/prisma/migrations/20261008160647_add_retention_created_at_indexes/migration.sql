-- Retention sweeps (#681): each purge reads "the oldest rows older than the
-- cutoff", across every user. Plain btree indexes on the timestamp, so a batch
-- is an index range scan rather than a sequential scan. `audit_events` already
-- has `audit_events_created_at_idx`.

-- CreateIndex
CREATE INDEX "notification_deliveries_created_at_idx" ON "notification_deliveries"("created_at");

-- CreateIndex
CREATE INDEX "notifications_created_at_idx" ON "notifications"("created_at");

-- CreateIndex
CREATE INDEX "ai_runs_created_at_idx" ON "ai_runs"("created_at");
