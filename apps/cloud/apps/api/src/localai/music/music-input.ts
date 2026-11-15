// The input side of `POST /api/localai/music/requests` and the prompt routes, as PURE functions: the form fields'
// schema, the cross-field rules (description, lyrics mode), the sample's type read from its bytes, and the generated
// title (#568). The limits are the agent's (E11 contract section 3).
import { z } from 'zod';

import { MusicLanguage, MusicSampleMode, utf8Bytes } from '../contracts/commands';
import {
  MUSIC_DEFAULT_STRENGTH,
  MUSIC_DESCRIPTION_MAX_CHARS,
  MUSIC_DURATION_MAX_S,
  MUSIC_DURATION_MIN_S,
  MUSIC_INSTRUMENTAL_LYRICS,
  MUSIC_LYRICS_MAX_BYTES,
  MUSIC_TAGS_MAX_BYTES,
  MUSIC_TITLE_MAX_CHARS,
  MUSIC_VARIATIONS_MAX,
} from './music-limits';

/** A lab node id (`ai-n2`). */
export const LabNodeId = z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/);

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

/** The three lyrics modes of the form (#568): the person's own, written by the AI, or none (instrumental). */
export const MusicLyricsMode = z.enum(['mine', 'ai', 'instrumental']);
export type MusicLyricsMode = z.infer<typeof MusicLyricsMode>;

/** The language of the page, for the "Untitled song" fallback title. */
export const UiLanguage = z.enum(['en', 'es']);
export type UiLanguage = z.infer<typeof UiLanguage>;

export const UuidText = z.string().regex(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i);

/** Characters as a person counts them (code points, so "ñ" is one). */
export const charCount = (value: string): number => [...value].length;

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/;

/** A song title: at most 80 characters, no control characters (it becomes a file name and a list label). Trimmed. */
export const TitleText = z
  .string()
  .refine((value) => !CONTROL.test(value), { message: 'the title has a control character' })
  .refine((value) => charCount(value.trim()) <= MUSIC_TITLE_MAX_CHARS, { message: 'the title is longer than 80 characters' })
  .transform((value) => value.trim());

/** "Describe your song": at most 500 characters AND at most 512 UTF-8 bytes (it is the command's `tags`). */
export const DescriptionText = z
  .string()
  .refine((value) => charCount(value) <= MUSIC_DESCRIPTION_MAX_CHARS, { message: 'the description is longer than 500 characters' })
  .refine((value) => utf8Bytes(value) <= MUSIC_TAGS_MAX_BYTES, { message: 'the description is longer than 512 bytes' });

/** Lyrics: at most 8 KiB of UTF-8. */
export const LyricsText = z.string().refine((value) => utf8Bytes(value) <= MUSIC_LYRICS_MAX_BYTES, { message: 'lyrics are longer than 8 KiB' });

/**
 * The text fields of the form. Strict: an unknown field is a 400, like the command contracts. A current page sends
 * `description` and `lyrics_mode`; an older client (the desktop app) sends `tags` and no mode, and keeps the old rule
 * (lyrics and tags may each be empty, not both). `promptId` makes the song from that saved prompt (the caller's own)
 * and saves the sent fields into it; without it a new prompt is saved. The cross-field rules are `musicSettingsOf`.
 */
export const MusicRequestFieldsSchema = z
  .object({
    nodeId: LabNodeId,
    lyrics: LyricsText.default(''),
    tags: z
      .string()
      .refine((value) => utf8Bytes(value) <= MUSIC_TAGS_MAX_BYTES, { message: 'tags are longer than 512 bytes' })
      .optional(),
    description: DescriptionText.optional(),
    lyrics_mode: MusicLyricsMode.optional(),
    title: TitleText.optional(),
    ui_language: UiLanguage.default('en'),
    promptId: UuidText.optional(),
    remove_sample: z
      .enum(['true', 'false'])
      .transform((value) => value === 'true')
      .default(false),
    duration_s: intField(MUSIC_DURATION_MIN_S, MUSIC_DURATION_MAX_S).default(60),
    language: MusicLanguage.default('en'),
    seed: intField(0, 2_147_483_647).optional(),
    variations: intField(1, MUSIC_VARIATIONS_MAX).default(1),
    sample_mode: MusicSampleMode.optional(),
    strength: numberField(0, 1).optional(),
  })
  .strict();

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

/** Control characters out, runs of white space folded, trimmed. */
const clean = (text: string): string =>
  // eslint-disable-next-line no-control-regex
  text.replace(/[\u0000-\u001f\u007f]/g, ' ').replace(/\s+/g, ' ').trim();

/** Every word starts with a capital; the rest of each word is kept as written (so "DJ" and "NASA" survive). */
export const titleCase = (text: string): string =>
  text.replace(/(^|[\s("'¿¡-])(\p{Ll})/gu, (_match, before: string, letter: string) => `${before}${letter.toLocaleUpperCase()}`);

/** Cut to the title limit (79 characters and an ellipsis), counting characters, not UTF-16 units. */
export const capTitle = (title: string): string => {
  const chars = [...title];
  return chars.length > MUSIC_TITLE_MAX_CHARS ? `${chars.slice(0, MUSIC_TITLE_MAX_CHARS - 1).join('').trimEnd()}…` : title;
};

/** The fallback title when there are no lyrics and no description. */
export const untitled = (ui: UiLanguage): string => (ui === 'es' ? 'Canción sin título' : 'Untitled song');

/** A title from elsewhere (an AI answer, a stored row) made safe: one line, no quotes around it, at most 80 characters. */
export function tidyTitle(text: string): string {
  const line = clean(text.split(/\r?\n/).find((candidate) => candidate.trim() !== '') ?? '');
  return capTitle(line.replace(/^["'“”‘’«»*#\s]+|["'“”‘’«»*\s.]+$/g, '').trim());
}

/**
 * The generated title (#568), deterministic: the first lyric line that is not a `[section]` tag, trimmed and
 * title-cased; else the first words of the description (up to its first comma or full stop, at most 10 words); else
 * "Untitled song" / "Canción sin título" by the page's language. At most 80 characters. Control characters removed.
 */
export function generatedTitle(lyrics: string, description: string, ui: UiLanguage = 'en'): string {
  const line = lyrics
    .split(/\r?\n/)
    .map((candidate) => clean(candidate.replace(/\[[^\]]*\]/g, '')))
    .find((candidate) => candidate !== '');
  if (line) return capTitle(titleCase(line));
  const firstClause = clean(description.split(/[,.;:\n]/)[0] ?? '');
  const words = firstClause
    .split(' ')
    .filter((word) => word !== '')
    .slice(0, 10)
    .join(' ');
  if (words) return capTitle(titleCase(words));
  return untitled(ui);
}

/** The settings a request sends and stores, after the cross-field rules of the form. */
export interface MusicSettings {
  title: string;
  description: string;
  lyricsMode: MusicLyricsMode;
  /** What the prompt keeps (the person's text, even in instrumental mode, so switching back loses nothing). */
  lyricsStored: string;
  /** What the node gets: `[Instrumental]` for "No vocals". */
  lyricsSent: string;
}

/** Why the cross-field rules refused the form; the 400's `reason`. */
export type MusicSettingsRefusal = 'description_and_tags' | 'description_required' | 'lyrics_required' | 'lyrics_or_tags_required';

/** What `musicSettingsOf` reads (the form's fields, or a prompt's columns under the form's names). */
export interface MusicSettingsInput {
  lyrics: string;
  tags?: string;
  description?: string;
  lyrics_mode?: MusicLyricsMode;
  title?: string;
  ui_language?: UiLanguage;
}

/**
 * The cross-field rules (#568). With `lyrics_mode` (the current page): a description is required, and "mine" and
 * "ai" need lyrics; "instrumental" sends `[Instrumental]`. Without it (an older client): lyrics or tags, not both
 * empty, and the mode is derived. An empty title is generated.
 */
export function musicSettingsOf(fields: MusicSettingsInput): MusicSettings | MusicSettingsRefusal {
  if (fields.description !== undefined && fields.tags !== undefined && fields.tags !== fields.description) return 'description_and_tags';
  const description = fields.description ?? fields.tags ?? '';
  const ui = fields.ui_language ?? 'en';
  let lyricsMode: MusicLyricsMode;
  let lyricsSent: string;
  if (fields.lyrics_mode !== undefined) {
    if (description.trim() === '') return 'description_required';
    lyricsMode = fields.lyrics_mode;
    if (lyricsMode !== 'instrumental' && fields.lyrics.trim() === '') return 'lyrics_required';
    lyricsSent = lyricsMode === 'instrumental' ? MUSIC_INSTRUMENTAL_LYRICS : fields.lyrics;
  } else {
    if (fields.lyrics.trim() === '' && description.trim() === '') return 'lyrics_or_tags_required';
    lyricsMode = fields.lyrics.trim() === '' ? 'instrumental' : 'mine';
    lyricsSent = fields.lyrics;
  }
  const typed = fields.title?.trim() ?? '';
  const title = typed !== '' ? typed : generatedTitle(lyricsMode === 'instrumental' ? '' : fields.lyrics, description, ui);
  return { title, description, lyricsMode, lyricsStored: fields.lyrics, lyricsSent };
}

/** The strength the node gets: the person's value, else the mode's default; none without a sample. */
export function strengthFor(mode: 'sounds_like' | 'same_song' | undefined, strength: number | undefined, hasSample: boolean): number | undefined {
  if (!hasSample) return undefined;
  return strength ?? MUSIC_DEFAULT_STRENGTH[mode ?? 'sounds_like'];
}
