-- The Android companion slice (#746, PP-9.4; merged from EvoPath and
-- MemoriaHub). Expand-only: one new table and one defaulted column.

-- CreateTable
CREATE TABLE "android_app_releases" (
    "id" UUID NOT NULL,
    "package_name" TEXT NOT NULL,
    "version_name" VARCHAR(50) NOT NULL,
    "version_code" INTEGER NOT NULL,
    "signing_sha256" TEXT NOT NULL,
    "file_sha256" CHAR(64) NOT NULL,
    "size_bytes" BIGINT NOT NULL,
    "storage_key" TEXT NOT NULL,
    "storage_provider" TEXT,
    "bucket" TEXT,
    "notes" VARCHAR(2000),
    "is_current" BOOLEAN NOT NULL DEFAULT false,
    "uploaded_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "android_app_releases_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "android_app_releases_created_at_idx" ON "android_app_releases"("created_at");

-- CreateIndex
CREATE INDEX "android_app_releases_uploaded_by_id_idx" ON "android_app_releases"("uploaded_by_id");

-- CreateIndex
CREATE UNIQUE INDEX "android_app_releases_package_name_version_code_key" ON "android_app_releases"("package_name", "version_code");

-- AddForeignKey
ALTER TABLE "android_app_releases" ADD CONSTRAINT "android_app_releases_uploaded_by_id_fkey" FOREIGN KEY ("uploaded_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- =============================================================================
-- Raw-SQL constraints (intentional schema drift: Prisma cannot express these).
-- Never declare android_app_releases_one_current_uniq_idx as @@unique and never
-- replace it with a findFirst pre-check; make-current clears the old flag and
-- sets the new one in one transaction, and the database rejects a second
-- current row (P2002, mapped to 409 RELEASE_CURRENT_CONFLICT).
-- =============================================================================

-- At most one current release deployment-wide.
CREATE UNIQUE INDEX "android_app_releases_one_current_uniq_idx" ON "android_app_releases" ((true)) WHERE "is_current";

-- Push subscription platform: tags each Web Push subscription with the surface
-- that registered it, so the `android_app` notification channel can target the
-- Android companion alone. Existing rows are browser subscriptions. A constant
-- default makes this a metadata-only ADD COLUMN (no table rewrite).

-- AlterTable
ALTER TABLE "push_subscriptions" ADD COLUMN "platform" TEXT NOT NULL DEFAULT 'browser';

-- Prisma cannot express a CHECK constraint, so it lives here only (intentional
-- schema drift, like the raw-SQL partial unique indexes). The contract's
-- PUSH_SUBSCRIPTION_PLATFORMS mirrors it.
ALTER TABLE "push_subscriptions"
  ADD CONSTRAINT "push_subscriptions_platform_check"
  CHECK ("platform" IN ('browser', 'android_app'));
