-- Auto song length (#571): a prompt and a request may have no length; null means the node's engine picks it.
-- AlterTable
ALTER TABLE "localai_music_prompts" ALTER COLUMN "duration_s" DROP NOT NULL;

-- AlterTable
ALTER TABLE "localai_music_requests" ALTER COLUMN "duration_s" DROP NOT NULL;
