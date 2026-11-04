-- =============================================================================
-- Notification Broadcasts (issue #320, epic #319)
-- =============================================================================
-- Adds `notification_broadcasts`. See the block comment above the
-- `NotificationBroadcast` model in prisma/schema.prisma for the full
-- reasoning behind every choice below (why a table at all rather than
-- `Job.payload`, why `channels` is a plain string array, why `status` carries
-- a currently-unreachable `draft` member, why `cursor_user_id` pairs with
-- `audience_cutoff` to freeze and replay the fan-out's audience, why
-- `created_by_id` is SetNull, and the rejected per-recipient join table) —
-- this file intentionally does not restate it at length.
-- =============================================================================

-- CreateEnum
CREATE TYPE "NotificationBroadcastStatus" AS ENUM ('draft', 'scheduled', 'sending', 'sent', 'canceled', 'failed');

-- CreateTable
CREATE TABLE "notification_broadcasts" (
    "id" UUID NOT NULL,
    "title" TEXT NOT NULL,
    "body" TEXT NOT NULL,
    "link" TEXT,
    "cta_label" TEXT,
    "event_key" TEXT NOT NULL,
    "channels" TEXT[],
    "status" "NotificationBroadcastStatus" NOT NULL DEFAULT 'scheduled',
    "scheduled_for" TIMESTAMPTZ,
    "started_at" TIMESTAMPTZ,
    "finished_at" TIMESTAMPTZ,
    "canceled_at" TIMESTAMPTZ,
    "audience_cutoff" TIMESTAMPTZ,
    "cursor_user_id" UUID,
    "recipients_targeted" INTEGER,
    "recipients_dispatched" INTEGER NOT NULL DEFAULT 0,
    "last_error" TEXT,
    "created_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "notification_broadcasts_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
-- "What is pending" — the scheduler's own query.
CREATE INDEX "notification_broadcasts_status_scheduled_for_idx" ON "notification_broadcasts"("status", "scheduled_for");

-- CreateIndex
-- The admin list page's default order, "most recently composed first".
CREATE INDEX "notification_broadcasts_created_at_idx" ON "notification_broadcasts"("created_at" DESC);

-- AddForeignKey
-- onDelete: SetNull — same audit-trail reasoning as
-- `audit_events_actor_user_id_fkey`: deleting the admin who composed a
-- broadcast must not delete the record of what they announced. Takes a
-- brief lock on `users` while the constraint is validated, as any new FK to
-- that table does.
ALTER TABLE "notification_broadcasts" ADD CONSTRAINT "notification_broadcasts_created_by_id_fkey" FOREIGN KEY ("created_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;
