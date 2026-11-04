-- AlterTable
ALTER TABLE "allowed_emails" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "audit_events" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "device_codes" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "permissions" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "refresh_tokens" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "roles" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "storage_object_chunks" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "storage_objects" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "system_settings" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "user_identities" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "user_settings" ALTER COLUMN "id" DROP DEFAULT;

-- AlterTable
ALTER TABLE "users" ALTER COLUMN "id" DROP DEFAULT;
