-- CreateEnum
CREATE TYPE "LocalaiMusicRequestStatus" AS ENUM ('queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled');

-- CreateEnum
CREATE TYPE "LocalaiMusicSongStatus" AS ENUM ('pending', 'ready', 'failed');

-- CreateTable
CREATE TABLE "localai_music_requests" (
    "id" UUID NOT NULL,
    "node_id" UUID NOT NULL,
    "command_id" UUID,
    "requested_by_id" UUID,
    "status" "LocalaiMusicRequestStatus" NOT NULL DEFAULT 'queued',
    "title" TEXT NOT NULL,
    "tags" TEXT NOT NULL,
    "lyrics_bytes" INTEGER NOT NULL,
    "duration_s" INTEGER NOT NULL,
    "language" TEXT NOT NULL,
    "seed" BIGINT,
    "variations" INTEGER NOT NULL,
    "sample_mode" TEXT,
    "strength" DOUBLE PRECISION,
    "sample_key" TEXT,
    "sample_mime" TEXT,
    "sample_bytes" INTEGER,
    "sample_sha256" TEXT,
    "error_code" TEXT,
    "error_message" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL,
    "finished_at" TIMESTAMPTZ,

    CONSTRAINT "localai_music_requests_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "localai_music_songs" (
    "id" UUID NOT NULL,
    "request_id" UUID NOT NULL,
    "index" INTEGER NOT NULL,
    "status" "LocalaiMusicSongStatus" NOT NULL DEFAULT 'pending',
    "storage_key" TEXT NOT NULL,
    "mime" TEXT,
    "size_bytes" INTEGER,
    "sha256" TEXT,
    "duration_s" DOUBLE PRECISION,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "ready_at" TIMESTAMPTZ,

    CONSTRAINT "localai_music_songs_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "localai_music_requests_command_id_key" ON "localai_music_requests"("command_id");

-- CreateIndex
CREATE UNIQUE INDEX "localai_music_requests_sample_key_key" ON "localai_music_requests"("sample_key");

-- CreateIndex
CREATE INDEX "localai_music_requests_requested_by_id_created_at_idx" ON "localai_music_requests"("requested_by_id", "created_at");

-- CreateIndex
CREATE INDEX "localai_music_requests_status_created_at_idx" ON "localai_music_requests"("status", "created_at");

-- CreateIndex
CREATE INDEX "localai_music_requests_node_id_created_at_idx" ON "localai_music_requests"("node_id", "created_at");

-- CreateIndex
CREATE UNIQUE INDEX "localai_music_songs_storage_key_key" ON "localai_music_songs"("storage_key");

-- CreateIndex
CREATE INDEX "localai_music_songs_status_created_at_idx" ON "localai_music_songs"("status", "created_at");

-- CreateIndex
CREATE UNIQUE INDEX "localai_music_songs_request_id_index_key" ON "localai_music_songs"("request_id", "index");

-- AddForeignKey
ALTER TABLE "localai_music_requests" ADD CONSTRAINT "localai_music_requests_node_id_fkey" FOREIGN KEY ("node_id") REFERENCES "localai_nodes"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_music_requests" ADD CONSTRAINT "localai_music_requests_command_id_fkey" FOREIGN KEY ("command_id") REFERENCES "localai_commands"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_music_requests" ADD CONSTRAINT "localai_music_requests_requested_by_id_fkey" FOREIGN KEY ("requested_by_id") REFERENCES "users"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "localai_music_songs" ADD CONSTRAINT "localai_music_songs_request_id_fkey" FOREIGN KEY ("request_id") REFERENCES "localai_music_requests"("id") ON DELETE CASCADE ON UPDATE CASCADE;
