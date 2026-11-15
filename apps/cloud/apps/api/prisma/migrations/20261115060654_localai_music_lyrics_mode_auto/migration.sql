-- The lyrics mode becomes an enum with a fourth value, `auto`, the new default (#570). The columns are converted in
-- place (USING), so every saved prompt and past request keeps its mode; a value that is not one of the four (none is
-- written by the app) reads as `auto` on a prompt and NULL on a request.
-- CreateEnum
CREATE TYPE "LocalaiMusicLyricsMode" AS ENUM ('auto', 'mine', 'ai', 'instrumental');

-- AlterTable
ALTER TABLE "localai_music_prompts" ALTER COLUMN "lyrics_mode" TYPE "LocalaiMusicLyricsMode"
  USING (CASE WHEN "lyrics_mode" IN ('auto', 'mine', 'ai', 'instrumental') THEN "lyrics_mode" ELSE 'auto' END)::"LocalaiMusicLyricsMode";
ALTER TABLE "localai_music_prompts" ALTER COLUMN "lyrics_mode" SET DEFAULT 'auto';

-- AlterTable
ALTER TABLE "localai_music_requests" ALTER COLUMN "lyrics_mode" TYPE "LocalaiMusicLyricsMode"
  USING (CASE WHEN "lyrics_mode" IN ('auto', 'mine', 'ai', 'instrumental') THEN "lyrics_mode" ELSE NULL END)::"LocalaiMusicLyricsMode";
