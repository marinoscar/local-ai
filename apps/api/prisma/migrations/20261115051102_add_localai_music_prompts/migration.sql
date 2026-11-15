-- Music prompt history and song titles (#568). The songs' new title is filled from their request's title before it
-- becomes NOT NULL, so existing rows keep the title the library already showed.
-- DropIndex
DROP INDEX "localai_music_requests_sample_key_key";

-- AlterTable
ALTER TABLE "localai_music_requests" ADD COLUMN     "lyrics_mode" TEXT,
ADD COLUMN     "prompt_id" UUID;

-- AlterTable
ALTER TABLE "localai_music_songs" ADD COLUMN     "title" TEXT;
UPDATE "localai_music_songs" AS s SET "title" = r."title" FROM "localai_music_requests" AS r WHERE s."request_id" = r."id";
UPDATE "localai_music_songs" SET "title" = 'Untitled song' WHERE "title" IS NULL;
ALTER TABLE "localai_music_songs" ALTER COLUMN "title" SET NOT NULL;

-- CreateTable
CREATE TABLE "localai_music_prompts" (
    "id" UUID NOT NULL,
    "owner_id" UUID NOT NULL,
    "title" TEXT NOT NULL,
    "description" TEXT NOT NULL,
    "lyrics_mode" TEXT NOT NULL,
    "lyrics" TEXT NOT NULL,
    "duration_s" INTEGER NOT NULL,
    "language" TEXT NOT NULL,
    "seed" BIGINT,
    "variations" INTEGER NOT NULL,
    "sample_key" TEXT,
    "sample_mime" TEXT,
    "sample_bytes" INTEGER,
    "sample_sha256" TEXT,
    "sample_mode" TEXT,
    "strength" DOUBLE PRECISION,
    "last_used_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,

    CONSTRAINT "localai_music_prompts_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "localai_music_prompts_owner_id_last_used_at_idx" ON "localai_music_prompts"("owner_id", "last_used_at");

-- CreateIndex
CREATE INDEX "localai_music_prompts_sample_key_idx" ON "localai_music_prompts"("sample_key");

-- CreateIndex
CREATE INDEX "localai_music_requests_prompt_id_idx" ON "localai_music_requests"("prompt_id");

-- CreateIndex
CREATE INDEX "localai_music_requests_sample_key_idx" ON "localai_music_requests"("sample_key");

-- AddForeignKey
ALTER TABLE "localai_music_requests" ADD CONSTRAINT "localai_music_requests_prompt_id_fkey" FOREIGN KEY ("prompt_id") REFERENCES "localai_music_prompts"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_music_prompts" ADD CONSTRAINT "localai_music_prompts_owner_id_fkey" FOREIGN KEY ("owner_id") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE CASCADE;
