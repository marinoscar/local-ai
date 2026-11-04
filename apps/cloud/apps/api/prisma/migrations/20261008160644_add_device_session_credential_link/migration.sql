-- =============================================================================
-- Device session <-> issued credential link (issue #518)
-- =============================================================================
-- Today nothing links a `DeviceCode` row to what it issued: once the polling
-- client collects a PAT (and that PAT is used to mint refresh tokens),
-- revoking the `DeviceCode` "session" does nothing to the credential the
-- device is actually holding. This migration adds the columns that let a
-- device-session revocation reach both:
--
-- 1. `device_codes.pat_id`: the PAT minted for this device once collected.
--    Nullable until collection. `SetNull` on delete — a `DeviceCode` history
--    row must survive its PAT being deleted (or aging out) independently.
-- 2. `device_codes.collected_at` / `credential_expires_at`: collection state
--    copied from the PAT at collection time, so the sessions list can filter
--    "approved, not yet collected" vs. "collected, still valid" without a
--    join against `personal_access_tokens`.
-- 3. `device_codes.revoked_at`: marks the device session (and whatever it
--    issued) as revoked. Distinct from `PersonalAccessToken.revokedAt` /
--    `RefreshToken.revokedAt`, which the revocation service also sets on the
--    linked rows — this column is what the sessions list itself filters on.
-- 4. `refresh_tokens.device_code_id`: the device-authorization session that
--    minted this refresh token, if any, so that same revocation can reach
--    refresh tokens too. `SetNull` on delete for the same reason as above.
--
-- Additive only: every new column is nullable, no backfill, no data migration.
-- =============================================================================

-- AlterTable
ALTER TABLE "device_codes"
  ADD COLUMN "pat_id" UUID,
  ADD COLUMN "collected_at" TIMESTAMPTZ,
  ADD COLUMN "credential_expires_at" TIMESTAMPTZ,
  ADD COLUMN "revoked_at" TIMESTAMPTZ;

-- AlterTable
ALTER TABLE "refresh_tokens" ADD COLUMN "device_code_id" UUID;

-- CreateIndex
-- Serves the sessions list: "this user's device sessions that are not
-- revoked" (approved-uncollected and collected-unexpired both satisfy
-- `revoked_at IS NULL`); the remaining collected/expiry filtering is done in
-- the query predicate, not a second index.
CREATE INDEX "device_codes_user_id_revoked_at_idx" ON "device_codes"("user_id", "revoked_at");

-- CreateIndex
CREATE INDEX "refresh_tokens_device_code_id_idx" ON "refresh_tokens"("device_code_id");

-- AddForeignKey
ALTER TABLE "device_codes" ADD CONSTRAINT "device_codes_pat_id_fkey" FOREIGN KEY ("pat_id") REFERENCES "personal_access_tokens"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "refresh_tokens" ADD CONSTRAINT "refresh_tokens_device_code_id_fkey" FOREIGN KEY ("device_code_id") REFERENCES "device_codes"("id") ON DELETE SET NULL ON UPDATE CASCADE;
