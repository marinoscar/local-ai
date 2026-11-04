-- CreateEnum
CREATE TYPE "MembershipStatus" AS ENUM ('active', 'suspended');

-- CreateEnum
CREATE TYPE "InviteStatus" AS ENUM ('pending', 'accepted', 'revoked', 'expired');

-- AlterTable
ALTER TABLE "device_codes" ADD COLUMN     "org_id" UUID;

-- AlterTable
ALTER TABLE "personal_access_tokens" ADD COLUMN     "org_id" UUID;

-- AlterTable
ALTER TABLE "refresh_tokens" ADD COLUMN     "org_id" UUID;

-- CreateTable
CREATE TABLE "organizations" (
    "id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "slug" TEXT NOT NULL,
    "is_default" BOOLEAN NOT NULL DEFAULT false,
    "created_by_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "organizations_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "memberships" (
    "id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "status" "MembershipStatus" NOT NULL DEFAULT 'active',
    "last_active_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "memberships_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "org_invites" (
    "id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "email" TEXT NOT NULL,
    "status" "InviteStatus" NOT NULL DEFAULT 'pending',
    "token_hash" TEXT,
    "expires_at" TIMESTAMPTZ,
    "invited_by_id" UUID,
    "accepted_by_id" UUID,
    "accepted_at" TIMESTAMPTZ,
    "notes" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "org_invites_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "organizations_slug_key" ON "organizations"("slug");

-- CreateIndex
CREATE INDEX "memberships_user_id_idx" ON "memberships"("user_id");

-- CreateIndex
CREATE UNIQUE INDEX "memberships_org_id_user_id_key" ON "memberships"("org_id", "user_id");

-- CreateIndex
CREATE UNIQUE INDEX "org_invites_token_hash_key" ON "org_invites"("token_hash");

-- CreateIndex
CREATE INDEX "org_invites_email_idx" ON "org_invites"("email");

-- CreateIndex
CREATE UNIQUE INDEX "org_invites_org_id_email_key" ON "org_invites"("org_id", "email");

-- CreateIndex
CREATE INDEX "device_codes_org_id_idx" ON "device_codes"("org_id");

-- CreateIndex
CREATE INDEX "personal_access_tokens_org_id_idx" ON "personal_access_tokens"("org_id");

-- CreateIndex
CREATE INDEX "refresh_tokens_org_id_idx" ON "refresh_tokens"("org_id");

-- AddForeignKey
ALTER TABLE "refresh_tokens" ADD CONSTRAINT "refresh_tokens_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "personal_access_tokens" ADD CONSTRAINT "personal_access_tokens_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "device_codes" ADD CONSTRAINT "device_codes_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "organizations" ADD CONSTRAINT "organizations_created_by_id_fkey" FOREIGN KEY ("created_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "memberships" ADD CONSTRAINT "memberships_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "memberships" ADD CONSTRAINT "memberships_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "org_invites" ADD CONSTRAINT "org_invites_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "org_invites" ADD CONSTRAINT "org_invites_invited_by_id_fkey" FOREIGN KEY ("invited_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "org_invites" ADD CONSTRAINT "org_invites_accepted_by_id_fkey" FOREIGN KEY ("accepted_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- =============================================================================
-- Raw SQL (hand-written; Prisma cannot express a partial unique index)
-- =============================================================================

-- Exactly one default organization: a constant-expression partial unique index.
-- Intentional schema drift: never replace it with @@unique or a findFirst check.
CREATE UNIQUE INDEX "organizations_default_uniq_idx" ON "organizations" ((true)) WHERE "is_default";

-- =============================================================================
-- Backfill (forward-only). Every INSERT names its id: the server-side uuid
-- defaults were dropped (0005_drop_stale_uuid_defaults).
-- =============================================================================

-- 1. The default organization.
INSERT INTO "organizations" ("id", "name", "slug", "is_default", "created_at", "updated_at")
VALUES (gen_random_uuid(), 'Default organization', 'default', true, now(), now())
ON CONFLICT DO NOTHING;

-- 2. One active membership per existing user.
INSERT INTO "memberships" ("id", "org_id", "user_id", "status", "last_active_at", "created_at", "updated_at")
SELECT gen_random_uuid(), o."id", u."id", 'active', now(), now(), now()
FROM "users" u
CROSS JOIN "organizations" o
WHERE o."is_default"
ON CONFLICT ("org_id", "user_id") DO NOTHING;

-- 3. Every existing credential row acts in the default organization.
UPDATE "refresh_tokens" SET "org_id" = (SELECT "id" FROM "organizations" WHERE "is_default") WHERE "org_id" IS NULL;
UPDATE "personal_access_tokens" SET "org_id" = (SELECT "id" FROM "organizations" WHERE "is_default") WHERE "org_id" IS NULL;
UPDATE "device_codes" SET "org_id" = (SELECT "id" FROM "organizations" WHERE "is_default") WHERE "org_id" IS NULL;
