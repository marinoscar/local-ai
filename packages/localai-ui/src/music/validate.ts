// The limits of `POST /music/generate` (E11 contract section 3, docs/specs/music.md). The node checks them again and
// answers 400/413 LAI-SVC-154; the form checks first so a person sees the problem next to the field.
import type { GenerateRequest, SampleMode } from './types';

export const LYRICS_MAX_BYTES = 8 * 1024;
export const TAGS_MAX_BYTES = 512;
export const DURATION_MIN_S = 10;
export const DURATION_MAX_S = 240;
export const DURATION_DEFAULT_S = 60;
export const VARIATIONS_MAX = 4;
export const SAMPLE_MAX_BYTES = 20 * 1024 * 1024;
export const SAMPLE_MAX_S = 60;
export const SEED_MAX = 4294967295;
/** The extensions a sample may have; the node also checks its content. */
export const SAMPLE_EXTENSIONS = ['wav', 'mp3', 'flac', 'ogg'] as const;
/** For the file picker's `accept`. */
export const SAMPLE_ACCEPT = '.wav,.mp3,.flac,.ogg,audio/wav,audio/x-wav,audio/mpeg,audio/flac,audio/ogg';
/** The code of a rejected input (the same one the node answers). */
export const CODE_INPUT_REJECTED = 'LAI-SVC-154';

/** The default strength of a sample mode (contract: 1.0 for `same_song`, 0.5 for `sounds_like`). */
export function defaultStrength(mode: SampleMode): number {
  return mode === 'same_song' ? 1 : 0.5;
}

/** The section tags the lyrics helper inserts. */
export const SECTION_TAGS = ['verse', 'chorus', 'bridge'] as const;
export type SectionTag = (typeof SECTION_TAGS)[number];

/**
 * insertSection puts `[tag]` on its own line at the caret: a newline before it when the caret is not at a line start,
 * one after it. Returns the new text and where the caret goes (after the tag line).
 */
export function insertSection(text: string, start: number, end: number, tag: SectionTag): { text: string; caret: number } {
  const s = Math.max(0, Math.min(start, text.length));
  const e = Math.max(s, Math.min(end, text.length));
  const before = text.slice(0, s);
  const after = text.slice(e);
  const lead = before.length > 0 && !before.endsWith('\n') ? '\n' : '';
  const block = `${lead}[${tag}]\n`;
  return { text: before + block + after, caret: before.length + block.length };
}

/** The UTF-8 size of a string, the unit the node's limits use. */
export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

export type FieldName = 'lyrics' | 'tags' | 'duration_s' | 'language' | 'seed' | 'variations' | 'sample' | 'strength';

/** One problem, as a key of the studio's strings and its values. */
export interface FieldError {
  field: FieldName;
  key:
    | 'errNothing'
    | 'errLyricsLong'
    | 'errTagsLong'
    | 'errDuration'
    | 'errSeed'
    | 'errVariations'
    | 'errSampleType'
    | 'errSampleBig'
    | 'errStrength';
  vars?: Record<string, string | number>;
}

/** The raw form, as the inputs hold it (numbers may still be text). */
export interface StudioForm {
  lyrics: string;
  tags: string;
  duration: string;
  language: string;
  seed: string;
  variations: string;
  sample: File | null;
  sampleMode: SampleMode;
  /** null: the mode's default. */
  strength: number | null;
}

function extOf(name: string): string {
  const i = name.lastIndexOf('.');
  return i < 0 ? '' : name.slice(i + 1).toLowerCase();
}

function intOf(s: string): number | null {
  const t = s.trim();
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return Number.isSafeInteger(n) ? n : null;
}

/** validateForm returns every problem of the form (empty: it can be sent). */
export function validateForm(f: StudioForm): FieldError[] {
  const out: FieldError[] = [];
  if (f.lyrics.trim() === '' && f.tags.trim() === '') out.push({ field: 'lyrics', key: 'errNothing' });
  if (byteLength(f.lyrics) > LYRICS_MAX_BYTES) out.push({ field: 'lyrics', key: 'errLyricsLong', vars: { max: LYRICS_MAX_BYTES } });
  if (byteLength(f.tags) > TAGS_MAX_BYTES) out.push({ field: 'tags', key: 'errTagsLong', vars: { max: TAGS_MAX_BYTES } });
  const d = intOf(f.duration);
  if (d === null || d < DURATION_MIN_S || d > DURATION_MAX_S)
    out.push({ field: 'duration_s', key: 'errDuration', vars: { min: DURATION_MIN_S, max: DURATION_MAX_S } });
  if (f.seed.trim() !== '') {
    const s = intOf(f.seed);
    if (s === null || s > SEED_MAX) out.push({ field: 'seed', key: 'errSeed' });
  }
  const v = intOf(f.variations);
  if (v === null || v < 1 || v > VARIATIONS_MAX) out.push({ field: 'variations', key: 'errVariations', vars: { max: VARIATIONS_MAX } });
  if (f.sample) {
    if (!(SAMPLE_EXTENSIONS as readonly string[]).includes(extOf(f.sample.name))) out.push({ field: 'sample', key: 'errSampleType' });
    else if (f.sample.size > SAMPLE_MAX_BYTES) out.push({ field: 'sample', key: 'errSampleBig', vars: { mb: SAMPLE_MAX_BYTES / (1024 * 1024) } });
    if (f.strength !== null && !(f.strength >= 0 && f.strength <= 1)) out.push({ field: 'strength', key: 'errStrength' });
  }
  return out;
}

/** toRequest builds the request of a valid form (call validateForm first). */
export function toRequest(f: StudioForm): GenerateRequest {
  const seed = f.seed.trim() === '' ? null : Number(f.seed.trim());
  return {
    lyrics: f.lyrics,
    tags: f.tags.trim(),
    duration_s: Number(f.duration.trim()),
    language: f.language,
    seed,
    variations: Number(f.variations.trim()),
    sample: f.sample,
    sample_mode: f.sample ? f.sampleMode : null,
    strength: f.sample ? (f.strength ?? defaultStrength(f.sampleMode)) : null,
  };
}
