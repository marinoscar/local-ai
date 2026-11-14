// Reads the multipart body of `POST /api/localai/music/requests`: the text fields and at most one file, `sample`
// (at most 20 MiB, read into memory: the store takes it in one upload and its sha256 goes to the node). The app's
// multipart plugin (`main.ts`, registered with the storage slice) parses; this applies the feature's own limits per
// request. Nothing here logs a field's value.
import { musicInputRejected, musicNotMultipart, musicSampleType, musicTooLarge } from './music-errors';
import { sniffAudio, type SniffedAudio } from './music-input';
import { MUSIC_MULTIPART_FIELD_MAX_BYTES, MUSIC_MULTIPART_MAX_FIELDS, MUSIC_SAMPLE_MAX_BYTES } from './music-limits';

/** One part as `@fastify/multipart` hands it out. */
type Part =
  | { type: 'field'; fieldname: string; value: unknown; valueTruncated?: boolean; fieldnameTruncated?: boolean }
  | { type: 'file'; fieldname: string; toBuffer(): Promise<Buffer>; file: NodeJS.ReadableStream & { resume(): void } };

interface MultipartCapable {
  isMultipart?: () => boolean;
  parts?: (options: { limits: Record<string, number> }) => AsyncIterableIterator<Part>;
}

export interface MusicForm {
  fields: Record<string, string>;
  sample: { bytes: Buffer; audio: SniffedAudio } | null;
}

const LIMIT_CODES = new Set(['FST_FILES_LIMIT', 'FST_FIELDS_LIMIT', 'FST_PARTS_LIMIT']);

/** The form of a request; throws the feature's 400/413/415 refusals (LAI-SVC-154). */
export async function readMusicForm(request: unknown): Promise<MusicForm> {
  const req = request as MultipartCapable;
  if (typeof req.parts !== 'function' || typeof req.isMultipart !== 'function' || !req.isMultipart()) throw musicNotMultipart();

  const fields: Record<string, string> = {};
  let sample: Buffer | null = null;
  try {
    const parts = req.parts({
      limits: {
        fileSize: MUSIC_SAMPLE_MAX_BYTES,
        files: 1,
        fields: MUSIC_MULTIPART_MAX_FIELDS,
        fieldSize: MUSIC_MULTIPART_FIELD_MAX_BYTES,
        parts: MUSIC_MULTIPART_MAX_FIELDS + 1,
      },
    });
    for await (const part of parts) {
      if (part.type === 'file') {
        if (part.fieldname !== 'sample') {
          part.file.resume();
          throw musicInputRejected(null, 'unknown_file_field');
        }
        sample = await part.toBuffer();
        if (sample.length === 0) sample = null;
        continue;
      }
      if (part.valueTruncated) throw musicTooLarge('field');
      if (part.fieldnameTruncated || Object.prototype.hasOwnProperty.call(fields, part.fieldname)) throw musicInputRejected(null, 'duplicate_field');
      fields[part.fieldname] = typeof part.value === 'string' ? part.value : String(part.value ?? '');
    }
  } catch (error) {
    const code = (error as { code?: unknown }).code;
    if (code === 'FST_REQ_FILE_TOO_LARGE') throw musicTooLarge('sample');
    if (typeof code === 'string' && LIMIT_CODES.has(code)) throw musicInputRejected(null, 'too_many_parts');
    if (code === 'FST_INVALID_MULTIPART_CONTENT_TYPE') throw musicNotMultipart();
    throw error;
  }

  if (sample === null) return { fields, sample: null };
  const audio = sniffAudio(sample.subarray(0, 16));
  if (!audio) throw musicSampleType();
  return { fields, sample: { bytes: sample, audio } };
}
