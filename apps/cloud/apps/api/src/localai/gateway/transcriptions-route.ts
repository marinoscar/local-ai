// `POST /v1/audio/transcriptions` (docs/specs/gateway.md sections 3.2, 4.5 and 6): the multipart upload of an audio file,
// forwarded to llama-server's OpenAI transcription route on a node whose model accepts audio. NOT whisper.cpp: the
// node's whisper server is loopback-only and is never exposed through Tailscale.
//
//   multipart (this file) -> model + scope (GatewayService) -> audio check -> limits -> re-encode -> attempt loop
//
// READING THE UPLOAD. `@fastify/multipart` is registered for THIS route only (a child scope of the gateway scope; when the
// application already registered the plugin for its own upload routes, the per-call limits below apply all the same):
// one file of at most 25 MiB, at most 10 other fields. The file is read into memory (never to disk) because a retry on
// another node needs the same bytes again; that is the one buffer, bounded by the file limit. Fields are checked against an
// allowlist; `model` is the only field the gateway changes.
//
// RE-ENCODING. The upstream body is a NEW multipart body with a new random boundary: the same fields in the same order
// with `model` replaced by the resolved catalog id, and the file part last with its bytes untouched. Nothing of the
// upload is ever logged or recorded: the usage row has `endpoint: "audio.transcriptions"`, `usageSource: "none"`, 0 tokens.
import { randomBytes } from 'node:crypto';

import multipart from '@fastify/multipart';
import type { FastifyInstance, FastifyRequest } from 'fastify';

import { GATEWAY_AUDIO_BODY_LIMIT } from './body';
import { GatewayError } from './errors';
import type { GatewayService } from './gateway.service';
import type { GatewayHandlers } from './routes';
import type { SelectedCandidate } from './routing/routing.types';

/** The largest file part, in bytes (spec section 6). */
export const AUDIO_FILE_LIMIT = GATEWAY_AUDIO_BODY_LIMIT;
/** Fields besides the file. */
export const AUDIO_FIELDS_LIMIT = 10;
/** One text field's value; `prompt` is the longest legitimate one. */
export const AUDIO_FIELD_SIZE_LIMIT = 256 * 1024;

export const AUDIO_MULTIPART_LIMITS = { fileSize: AUDIO_FILE_LIMIT, files: 1, fields: AUDIO_FIELDS_LIMIT, fieldSize: AUDIO_FIELD_SIZE_LIMIT } as const;

/** The multipart fields the endpoint accepts (OpenAI's list). Anything else is 400 LAI-GW-003. */
export const TRANSCRIPTION_FIELDS: ReadonlySet<string> = new Set(['file', 'model', 'language', 'prompt', 'response_format', 'temperature', 'timestamp_granularities[]']);
/** The only field that may appear more than once. */
const REPEATABLE_FIELDS: ReadonlySet<string> = new Set(['timestamp_granularities[]']);

export interface UploadedFile {
  filename: string;
  /** The part's own `Content-Type` (`application/octet-stream` when absent or malformed). */
  mimetype: string;
  data: Buffer;
}

export interface TranscriptionUpload {
  /** The `model` exactly as sent. */
  model: string;
  /** Every non-file field in the order it arrived (`model` included). */
  fields: Array<{ name: string; value: string }>;
  file: UploadedFile;
}

const SAFE_NAME = /^[A-Za-z0-9_.[\]-]{1,64}$/;
const MIME = /^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}\/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}$/;

/** A client-chosen field name is echoed in `param` only when it is short and plain. */
const paramOf = (name: string): string | null => (SAFE_NAME.test(name) ? name : null);

const notMultipart = (message = 'The request body is not valid multipart/form-data. Send the audio as a "file" part and the model as a "model" field.'): GatewayError =>
  new GatewayError('LAI-GW-001', { message });

/** Maps what the multipart plugin and busboy throw to a gateway error. Only the error's CODE is read, never its message. */
export function multipartErrorToGatewayError(error: unknown): GatewayError {
  if (error instanceof GatewayError) return error;
  const code = typeof error === 'object' && error !== null ? (error as { code?: unknown }).code : undefined;
  if (code === 'FST_REQ_FILE_TOO_LARGE') return new GatewayError('LAI-GW-004');
  if (code === 'FST_FILES_LIMIT') return new GatewayError('LAI-GW-003', { param: 'file', message: 'Send exactly one audio file in the "file" part.' });
  if (code === 'FST_FIELDS_LIMIT' || code === 'FST_PARTS_LIMIT') {
    return new GatewayError('LAI-GW-003', { message: `The request has too many fields. Send the audio "file" and at most ${AUDIO_FIELDS_LIMIT} other fields.` });
  }
  if (code === 'FST_INVALID_MULTIPART_CONTENT_TYPE') return notMultipart('Send the request as multipart/form-data.');
  return notMultipart();
}

/**
 * Reads and checks the upload. The file is held in memory (at most {@link AUDIO_FILE_LIMIT}); the order of the parts does
 * not matter, so a file that arrives before the `model` field is simply buffered.
 *
 * @throws GatewayError 413 LAI-GW-004 (file over the limit), 400 LAI-GW-002 (no model), 400 LAI-GW-003 (unknown field,
 *         second file, repeated field), 400 LAI-GW-001 (not multipart, or no file).
 */
export async function readTranscriptionUpload(request: FastifyRequest): Promise<TranscriptionUpload> {
  const fields: Array<{ name: string; value: string }> = [];
  const seen = new Set<string>();
  let file: UploadedFile | null = null;

  try {
    for await (const part of request.parts({ limits: AUDIO_MULTIPART_LIMITS })) {
      if (part.type === 'file') {
        if (part.fieldname !== 'file') throw new GatewayError('LAI-GW-003', { param: paramOf(part.fieldname), message: 'Send the audio in the "file" part.' });
        if (file) throw new GatewayError('LAI-GW-003', { param: 'file', message: 'Send exactly one audio file in the "file" part.' });
        const data = await part.toBuffer();
        file = { filename: part.filename, mimetype: MIME.test(part.mimetype) ? part.mimetype : 'application/octet-stream', data };
        continue;
      }
      const name = part.fieldname;
      if (!TRANSCRIPTION_FIELDS.has(name) || name === 'file') {
        throw new GatewayError('LAI-GW-003', { param: paramOf(name), message: 'A field of the request is not supported on this endpoint. Allowed: model, language, prompt, response_format, temperature, timestamp_granularities[].' });
      }
      if (seen.has(name) && !REPEATABLE_FIELDS.has(name)) throw new GatewayError('LAI-GW-003', { param: name, message: `The field "${name}" was sent more than once.` });
      if (part.fieldnameTruncated || part.valueTruncated || typeof part.value !== 'string') {
        throw new GatewayError('LAI-GW-003', { param: name, message: `The field "${name}" is too long.` });
      }
      seen.add(name);
      fields.push({ name, value: part.value });
    }
  } catch (error) {
    throw multipartErrorToGatewayError(error);
  }

  const model = fields.find((field) => field.name === 'model')?.value;
  if (model === undefined || model.trim().length === 0) throw new GatewayError('LAI-GW-002', { param: 'model' });
  if (!file) throw notMultipart('The request has no "file" part. Send the audio as a multipart "file" field.');
  return { model, fields, file };
}

const asciiOnly = (value: string): string => value.replace(/[^\x20-\x7e]/g, '_');

/** A filename that cannot break out of the header line: no quotes, backslashes or control characters. */
export function safeFilename(filename: string): string {
  const cleaned = asciiOnly(filename.replace(/[\r\n"\\]/g, '_')).slice(0, 200);
  return cleaned.trim() === '' ? 'audio' : cleaned;
}

/** A boundary that occurs nowhere in the parts (128 random bits make a collision a formality; this checks anyway). */
function chooseBoundary(parts: readonly Buffer[]): string {
  for (;;) {
    const boundary = `----localai-gw-${randomBytes(16).toString('hex')}`;
    const marker = Buffer.from(boundary);
    if (!parts.some((part) => part.includes(marker))) return boundary;
  }
}

/**
 * The upstream multipart body: the same fields in the same order with `model` set to `modelId`, then the file part with
 * its bytes unchanged. Returns the body and its `Content-Type` (with the new boundary).
 */
export function buildTranscriptionBody(upload: TranscriptionUpload, modelId: string): { body: Buffer; contentType: string } {
  const fieldValues = upload.fields.map((field) => ({ name: field.name, value: field.name === 'model' ? modelId : field.value }));
  const boundary = chooseBoundary([upload.file.data, ...fieldValues.map((field) => Buffer.from(field.value, 'utf8'))]);

  const chunks: Buffer[] = [];
  for (const field of fieldValues) {
    chunks.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${field.name}"\r\n\r\n`, 'utf8'), Buffer.from(field.value, 'utf8'), Buffer.from('\r\n', 'utf8'));
  }
  chunks.push(
    Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="${safeFilename(upload.file.filename)}"\r\nContent-Type: ${upload.file.mimetype}\r\n\r\n`, 'utf8'),
    upload.file.data,
    Buffer.from(`\r\n--${boundary}--\r\n`, 'utf8'),
  );
  return { body: Buffer.concat(chunks), contentType: `multipart/form-data; boundary=${boundary}` };
}

/** Does a candidate's model accept audio? `inputModalities` is what the node reported (empty when it reported none). */
export const acceptsAudio = (candidate: Pick<SelectedCandidate, 'inputModalities'>): boolean => candidate.inputModalities.includes('audio');

/** What the route needs from the routing table to answer "no node of this model takes audio" before it spends a rate-limit slot. */
export interface AudioCapability {
  /** `audio`: some usable candidate accepts audio. `no_audio`: candidates exist and none does. `unknown`: no usable candidate. */
  availability(modelId: string): 'audio' | 'no_audio' | 'unknown';
}

export function audioCapabilityOf(table: { healthyCandidates(modelId: string): readonly Pick<SelectedCandidate, 'inputModalities'>[] }): AudioCapability {
  return {
    availability(modelId) {
      const candidates = table.healthyCandidates(modelId);
      if (candidates.length === 0) return 'unknown';
      return candidates.some(acceptsAudio) ? 'audio' : 'no_audio';
    },
  };
}

/**
 * Makes `request.parts()` available on a route's scope with this endpoint's limits. When the application registered the
 * plugin itself (its own uploads), the decorator is inherited and the per-call limits of {@link readTranscriptionUpload}
 * carry the rule; registering it a second time in the child scope would throw.
 */
export async function registerAudioMultipart(scope: FastifyInstance): Promise<void> {
  if (scope.hasRequestDecorator('parts')) return;
  await scope.register(multipart, { limits: { ...AUDIO_MULTIPART_LIMITS } });
}

/** The handler bound to `POST /v1/audio/transcriptions`. */
export function transcriptionsHandlers(service: Pick<GatewayService, 'handleUpload'>, audio: AudioCapability): Pick<GatewayHandlers, 'transcriptions'> {
  return {
    transcriptions: async (ctx) => {
      const upload = await readTranscriptionUpload(ctx.request);
      return service.handleUpload('audio.transcriptions', ctx, {
        requestedModel: upload.model,
        check: (resolved) => {
          if (audio.availability(resolved.modelId) === 'no_audio') {
            throw new GatewayError('LAI-GW-020', { param: 'model', message: 'That model does not accept audio. Pick a model whose input_modalities include audio from GET /v1/models.' });
          }
        },
        eligible: acceptsAudio,
        build: (modelId) => buildTranscriptionBody(upload, modelId),
      });
    },
  };
}
