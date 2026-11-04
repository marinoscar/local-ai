-- CreateTable
CREATE TABLE "credentials" (
    "id" UUID NOT NULL,
    "purpose" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "secret" TEXT NOT NULL,
    "hint" TEXT,
    "label" TEXT,
    "updated_by_user_id" UUID,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "credentials_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "credentials_purpose_name_key" ON "credentials"("purpose", "name");

-- AddForeignKey
ALTER TABLE "credentials" ADD CONSTRAINT "credentials_updated_by_user_id_fkey" FOREIGN KEY ("updated_by_user_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;
