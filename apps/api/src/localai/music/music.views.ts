// The wire shapes of `/api/localai/music/*` for the web app (E11, docs/specs/cloud-app.md section 3.9). Never the
// lyrics, a storage key or a signed URL (the audio route hands a URL out on its own, short-lived).
import type { LocalaiMusicPrompt, LocalaiMusicRequest, LocalaiMusicSong } from '@prisma/client';
import { z } from 'zod';

import { MusicLanguage, MusicSampleMode } from '../contracts/commands';
import { DescriptionText, LabNodeId, LyricsText, MusicLyricsMode, TitleText, UiLanguage, UuidText } from './music-input';
import { MUSIC_DURATION_MAX_S, MUSIC_DURATION_MIN_S, MUSIC_VARIATIONS_MAX } from './music-limits';
import type { MusicReadiness, MusicStage } from './music-state';
import { stageOf } from './music-state';

export interface MusicSongView {
  id: string;
  index: number;
  status: LocalaiMusicSong['status'];
  mime: string | null;
  sizeBytes: number | null;
  durationS: number | null;
  readyAt: string | null;
}

export interface MusicRequestView {
  id: string;
  /** The lab node id (`ai-n2`). */
  nodeId: string;
  nodeName: string;
  status: LocalaiMusicRequest['status'];
  /** What the progress line says: queued on the node, generating, done, or how it ended. */
  stage: MusicStage;
  title: string;
  tags: string;
  /** The saved prompt it was made from; null once that prompt is deleted (the song stays). */
  promptId: string | null;
  lyricsMode: string | null;
  lyricsBytes: number;
  durationS: number;
  language: string;
  seed: number | null;
  variations: number;
  hasSample: boolean;
  sampleMode: string | null;
  strength: number | null;
  commandId: string | null;
  error: { code: string | null; message: string | null } | null;
  requestedByMe: boolean;
  /** The lyrics, read from the command's params: only to their author, only on the single-request route. */
  lyrics?: string;
  createdAt: string;
  finishedAt: string | null;
  songs: MusicSongView[];
}

export interface MusicLibrarySong extends MusicSongView {
  requestId: string;
  /** The saved prompt the song was made from; null when it was deleted (the page says "prompt deleted"). */
  promptId: string | null;
  nodeId: string;
  nodeName: string;
  title: string;
  tags: string;
  language: string;
  seed: number | null;
  createdAt: string;
  requestedByMe: boolean;
}

export interface Page<T> {
  items: T[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

export interface MusicNodeView {
  nodeId: string;
  nodeName: string;
  readiness: MusicReadiness;
  /** The music models on the node (catalog ids), loaded first. */
  musicModels: string[];
  /** The node's active workload (its snapshot), else the one it last switched to through the cloud, else null. */
  activeWorkload: string | null;
  /** The node's workloads from its last snapshot; null for an agent that does not report them (older than E11). */
  workloads: Array<{ name: string; label: { en: string; es: string }; fits: boolean; missing: string[] }> | null;
  lastStateAt: string | null;
}

/** One saved prompt in the history list (#568): no lyrics (they come with the single prompt). */
export interface MusicPromptSummary {
  id: string;
  title: string;
  description: string;
  lyricsMode: string;
  hasSample: boolean;
  /** Ready songs made from it (deleted songs are not counted). */
  songCount: number;
  lastUsedAt: string;
  createdAt: string;
  updatedAt: string;
}

/** One saved prompt, for its owner: everything the form needs to load it. */
export interface MusicPromptView extends MusicPromptSummary {
  lyrics: string;
  durationS: number;
  language: string;
  seed: number | null;
  variations: number;
  sampleMime: string | null;
  sampleBytes: number | null;
  sampleMode: string | null;
  strength: number | null;
}

const iso = (date: Date | null): string | null => (date ? date.toISOString() : null);

/** A stored seed (BigInt) as a JSON number: seeds are at most 2^32 - 1, well inside 2^53. */
export const seedOf = (seed: bigint | null): number | null => (seed === null ? null : Number(seed));

export function toSongView(song: LocalaiMusicSong): MusicSongView {
  return {
    id: song.id,
    index: song.index,
    status: song.status,
    mime: song.mime,
    sizeBytes: song.sizeBytes,
    durationS: song.durationS,
    readyAt: iso(song.readyAt),
  };
}

export type RequestRow = LocalaiMusicRequest & { songs: LocalaiMusicSong[]; node: { nodeId: string; displayName: string } };

export function toRequestView(row: RequestRow, viewerId: string): MusicRequestView {
  const error = row.errorCode !== null || row.errorMessage !== null ? { code: row.errorCode, message: row.errorMessage } : null;
  return {
    id: row.id,
    nodeId: row.node.nodeId,
    nodeName: row.node.displayName || row.node.nodeId,
    status: row.status,
    stage: stageOf(row.status),
    title: row.title,
    tags: row.tags,
    promptId: row.promptId,
    lyricsMode: row.lyricsMode,
    lyricsBytes: row.lyricsBytes,
    durationS: row.durationS,
    language: row.language,
    seed: seedOf(row.seed),
    variations: row.variations,
    hasSample: row.sampleKey !== null,
    sampleMode: row.sampleMode,
    strength: row.strength,
    commandId: row.commandId,
    error,
    requestedByMe: row.requestedById === viewerId,
    createdAt: row.createdAt.toISOString(),
    finishedAt: iso(row.finishedAt),
    songs: [...row.songs].sort((a, b) => a.index - b.index).map(toSongView),
  };
}

export function toPromptSummary(row: LocalaiMusicPrompt, songCount: number): MusicPromptSummary {
  return {
    id: row.id,
    title: row.title,
    description: row.description,
    lyricsMode: row.lyricsMode,
    hasSample: row.sampleKey !== null,
    songCount,
    lastUsedAt: row.lastUsedAt.toISOString(),
    createdAt: row.createdAt.toISOString(),
    updatedAt: row.updatedAt.toISOString(),
  };
}

export function toPromptView(row: LocalaiMusicPrompt, songCount: number): MusicPromptView {
  return {
    ...toPromptSummary(row, songCount),
    lyrics: row.lyrics,
    durationS: row.durationS,
    language: row.language,
    seed: seedOf(row.seed),
    variations: row.variations,
    sampleMime: row.sampleKey !== null ? row.sampleMime : null,
    sampleBytes: row.sampleKey !== null ? row.sampleBytes : null,
    sampleMode: row.sampleMode,
    strength: row.strength,
  };
}

export type SongRow = LocalaiMusicSong & {
  request: LocalaiMusicRequest & { node: { nodeId: string; displayName: string } };
};

export function toLibrarySong(row: SongRow, viewerId: string): MusicLibrarySong {
  return {
    ...toSongView(row),
    requestId: row.requestId,
    promptId: row.request.promptId,
    nodeId: row.request.node.nodeId,
    nodeName: row.request.node.displayName || row.request.node.nodeId,
    title: row.title,
    tags: row.request.tags,
    language: row.request.language,
    seed: seedOf(row.request.seed),
    createdAt: row.createdAt.toISOString(),
    requestedByMe: row.request.requestedById === viewerId,
  };
}

// --- query and body schemas (strict: an unknown parameter is a 400) ---------------------------------------------

const pageField = z
  .string()
  .regex(/^\d{1,6}$/)
  .transform(Number)
  .pipe(z.number().int().min(1));
const pageSizeField = z
  .string()
  .regex(/^\d{1,3}$/)
  .transform(Number)
  .pipe(z.number().int().min(1).max(50));

export const ListRequestsQuerySchema = z
  .object({
    page: pageField.default(1),
    pageSize: pageSizeField.default(20),
    mine: z.enum(['true', 'false']).transform((value) => value === 'true').optional(),
    active: z.enum(['true', 'false']).transform((value) => value === 'true').optional(),
  })
  .strict();

export const ListSongsQuerySchema = z
  .object({
    page: pageField.default(1),
    pageSize: pageSizeField.default(20),
    nodeId: z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/).optional(),
    mine: z.enum(['true', 'false']).transform((value) => value === 'true').optional(),
  })
  .strict();

export const AudioQuerySchema = z
  .object({
    download: z.enum(['0', '1', 'true', 'false']).transform((value) => value === '1' || value === 'true').default(false),
    as: z.enum(['redirect', 'json']).default('redirect'),
  })
  .strict();

export const DeleteSongBodySchema = z.object({ confirm: z.string().max(64) }).strict();

export const ActivateWorkloadBodySchema = z.object({ force: z.boolean().optional() }).strict();

// --- the prompt routes (#568), JSON bodies, strict ---------------------------------------------------------------

export const ListPromptsQuerySchema = z
  .object({
    page: pageField.default(1),
    pageSize: pageSizeField.default(20),
  })
  .strict();

const Seed = z.number().int().min(0).max(2_147_483_647);
const Duration = z.number().int().min(MUSIC_DURATION_MIN_S).max(MUSIC_DURATION_MAX_S);
const Variations = z.number().int().min(1).max(MUSIC_VARIATIONS_MAX);
const Strength = z.number().min(0).max(1);

/** `POST /prompts`: a new saved prompt ("New prompt" then "Save", or "Save as new"). A description is required. */
export const CreatePromptBodySchema = z
  .object({
    title: TitleText.optional(),
    description: DescriptionText.refine((value) => value.trim() !== '', { message: 'describe the song' }),
    lyricsMode: MusicLyricsMode.default('auto'),
    lyrics: LyricsText.default(''),
    durationS: Duration.default(60),
    language: MusicLanguage.default('en'),
    seed: Seed.nullable().optional(),
    variations: Variations.default(1),
    sampleMode: MusicSampleMode.nullable().optional(),
    strength: Strength.nullable().optional(),
    uiLanguage: UiLanguage.default('en'),
    /** "Save as new" from a prompt with a sample: reuse that prompt's sample (the caller's own prompt). */
    copySampleFrom: UuidText.optional(),
  })
  .strict();

/** `PATCH /prompts/:id`: any of the fields; `title: ""` generates one; `removeSample: true` drops the sample. */
export const UpdatePromptBodySchema = z
  .object({
    title: TitleText.optional(),
    description: DescriptionText.refine((value) => value.trim() !== '', { message: 'describe the song' }).optional(),
    lyricsMode: MusicLyricsMode.optional(),
    lyrics: LyricsText.optional(),
    durationS: Duration.optional(),
    language: MusicLanguage.optional(),
    seed: Seed.nullable().optional(),
    variations: Variations.optional(),
    sampleMode: MusicSampleMode.nullable().optional(),
    strength: Strength.nullable().optional(),
    uiLanguage: UiLanguage.default('en'),
    removeSample: z.literal(true).optional(),
  })
  .strict()
  .refine((body) => Object.keys(body).some((key) => key !== 'uiLanguage'), { message: 'nothing to change' });

/** `POST /prompts/:id/make`: the node, and overrides for this one song (the prompt itself is not changed). */
export const MakePromptBodySchema = z
  .object({
    nodeId: LabNodeId,
    overrides: z
      .object({
        variations: Variations.optional(),
        seed: Seed.nullable().optional(),
        durationS: Duration.optional(),
        language: MusicLanguage.optional(),
      })
      .strict()
      .optional(),
  })
  .strict();

export const DeletePromptBodySchema = z.object({ confirm: z.string().max(64) }).strict();

/** `POST /title-suggestion`. */
export const TitleSuggestionBodySchema = z
  .object({
    lyrics: LyricsText.default(''),
    description: DescriptionText.default(''),
    lyricsMode: MusicLyricsMode.optional(),
    language: MusicLanguage.default('en'),
    uiLanguage: UiLanguage.default('en'),
  })
  .strict();

/** `POST /lyrics-suggestion`. */
export const LyricsSuggestionBodySchema = z
  .object({
    description: DescriptionText.refine((value) => value.trim() !== '', { message: 'describe the song' }),
    language: MusicLanguage.default('en'),
    title: TitleText.optional(),
  })
  .strict();

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
