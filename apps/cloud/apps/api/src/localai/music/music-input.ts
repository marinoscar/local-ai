// The input side of `POST /api/localai/music/requests`, as PURE functions: the form fields' schema, the sample's type
// read from its bytes, and the library title. The limits are the agent's (E11 contract section 3).
import { z } from 'zod';

import { MusicLanguage, MusicSampleMode, utf8Bytes } from '../contracts/commands';
import {
  MUSIC_DEFAULT_STRENGTH,
  MUSIC_DURATION_MAX_S,
  MUSIC_DURATION_MIN_S,
  MUSIC_LYRICS_MAX_BYTES,
  MUSIC_TAGS_MAX_BYTES,
  MUSIC_TITLE_MAX_CHARS,
  MUSIC_VARIATIONS_MAX,
} from './music-limits';

/** A lab node id (`ai-n2`). */
const LabNodeId = z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/);

/** A form field holding an integer (multipart fields are text). */
const intField = (min: number, max: number) =>
  z
    .string()
    .regex(/^-?\d{1,10}$/, { message: 'must be a whole number' })
    .transform(Number)
    .pipe(z.number().int().min(min).max(max));

const numberField = (min: number, max: number) =>
  z
    .string()
    .regex(/^\d+(\.\d+)?$/, { message: 'must be a number' })
    .transform(Number)
    .pipe(z.number().min(min).max(max));

/**
 * The text fields of the form. Strict: an unknown field is a 400, like the command contracts. `lyrics` and `tags` may
 * each be empty, but not both (an instrumental takes tags only).
 */
export const MusicRequestFieldsSchema = z
  .object({
    nodeId: LabNodeId,
    lyrics: z
      .string()
      .refine((value) => utf8Bytes(value) <= MUSIC_LYRICS_MAX_BYTES, { message: 'lyrics are longer than 8 KiB' })
      .default(''),
    tags: z
      .string()
      .refine((value) => utf8Bytes(value) <= MUSIC_TAGS_MAX_BYTES, { message: 'tags are longer than 512 bytes' })
      .default(''),
    duration_s: intField(MUSIC_DURATION_MIN_S, MUSIC_DURATION_MAX_S).default(60),
    language: MusicLanguage.default('en'),
    seed: intField(0, 4_294_967_295).optional(),
    variations: intField(1, MUSIC_VARIATIONS_MAX).default(1),
    sample_mode: MusicSampleMode.optional(),
    strength: numberField(0, 1).optional(),
  })
  .strict()
  .refine((fields) => fields.lyrics.trim() !== '' || fields.tags.trim() !== '', {
    message: 'write some lyrics or a style',
    path: ['lyrics'],
  });

export type MusicRequestFields = z.output<typeof MusicRequestFieldsSchema>;

/** The sample's media type and file extension, read from its first bytes. */
export interface SniffedAudio {
  mime: 'audio/wav' | 'audio/mpeg' | 'audio/flac' | 'audio/ogg';
  ext: 'wav' | 'mp3' | 'flac' | 'ogg';
}

/**
 * The type of an audio file from its bytes: RIFF/WAVE, fLaC, OggS, or MP3 (an ID3 tag or an MPEG frame sync). The
 * client's file name and declared type are ignored. Null for anything else.
 */
export function sniffAudio(bytes: Uint8Array): SniffedAudio | null {
  const ascii = (start: number, length: number) => String.fromCharCode(...bytes.subarray(start, start + length));
  if (bytes.length >= 12 && ascii(0, 4) === 'RIFF' && ascii(8, 4) === 'WAVE') return { mime: 'audio/wav', ext: 'wav' };
  if (bytes.length >= 4 && ascii(0, 4) === 'fLaC') return { mime: 'audio/flac', ext: 'flac' };
  if (bytes.length >= 4 && ascii(0, 4) === 'OggS') return { mime: 'audio/ogg', ext: 'ogg' };
  if (bytes.length >= 3 && ascii(0, 3) === 'ID3') return { mime: 'audio/mpeg', ext: 'mp3' };
  // An MPEG audio frame: 11 sync bits, a valid layer (not "reserved") and not the "free"/bad bitrate indexes.
  if (bytes.length >= 4 && bytes[0] === 0xff && (bytes[1] & 0xe0) === 0xe0 && (bytes[1] & 0x06) !== 0 && (bytes[2] & 0xf0) !== 0xf0) {
    return { mime: 'audio/mpeg', ext: 'mp3' };
  }
  return null;
}

/** The file extension for a song's stored type (the agent reports it; wav by default). */
export function extensionOf(mime: string | null): string {
  switch (mime) {
    case 'audio/mpeg':
      return 'mp3';
    case 'audio/flac':
      return 'flac';
    case 'audio/ogg':
      return 'ogg';
    default:
      return 'wav';
  }
}

/**
 * The library title: the first lyric line that is not a structure tag (`[verse]`, `[chorus]`), else the tags, else
 * "Untitled", cut to 120 characters. Control characters are removed.
 */
export function songTitle(lyrics: string, tags: string): string {
  const clean = (text: string) =>
    // eslint-disable-next-line no-control-regex
    text.replace(/[\u0000-\u001f\u007f]/g, ' ').replace(/\s+/g, ' ').trim();
  const line = lyrics
    .split(/\r?\n/)
    .map((candidate) => clean(candidate.replace(/\[[^\]]*\]/g, '')))
    .find((candidate) => candidate !== '');
  const title = line ?? (clean(tags) || 'Untitled');
  return title.length > MUSIC_TITLE_MAX_CHARS ? `${title.slice(0, MUSIC_TITLE_MAX_CHARS - 1)}…` : title;
}

/** The strength the node gets: the person's value, else the mode's default; none without a sample. */
export function strengthFor(mode: 'sounds_like' | 'same_song' | undefined, strength: number | undefined, hasSample: boolean): number | undefined {
  if (!hasSample) return undefined;
  return strength ?? MUSIC_DEFAULT_STRENGTH[mode ?? 'sounds_like'];
}
