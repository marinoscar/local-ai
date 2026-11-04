-- =============================================================================
-- Per-user encrypted credentials (issue #387)
-- =============================================================================
-- A sibling of "credentials", which is NOT modified. Rows are addressed by
-- (user_id, purpose, name); `secret` is ciphertext encrypted under the
-- owner-bound sub-key domain `user:<user_id>:<purpose>`. All three unique
-- columns are NOT NULL so the unique index is a real constraint. Rows cascade
-- with their owner.

-- CreateTable
CREATE TABLE "user_credentials" (
    "id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "purpose" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "secret" TEXT NOT NULL,
    "hint" TEXT,
    "label" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "user_credentials_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "user_credentials_user_id_purpose_name_key" ON "user_credentials"("user_id", "purpose", "name");

-- AddForeignKey
ALTER TABLE "user_credentials" ADD CONSTRAINT "user_credentials_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;
