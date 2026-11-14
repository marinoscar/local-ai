// The refusals of the music routes in EAB's error format: the HTTP status and message are EAB's, and the doctor code
// (E11 contract section 7) and a machine-readable reason travel in `details`. A message never echoes the lyrics, the
// tags, the sample or a URL.
import {
  BadRequestException,
  ConflictException,
  HttpException,
  HttpStatus,
  NotFoundException,
  ServiceUnavailableException,
} from '@nestjs/common';
import type { ZodError } from 'zod';

import { wireIssues } from '../commands/command-errors';

/** Sample or input rejected (type, size, length). */
export const LAI_SVC_INPUT_REJECTED = 'LAI-SVC-154';
/** Music queue full (here: the person's own share of it). */
export const LAI_SVC_QUEUE_FULL = 'LAI-SVC-153';

/** `400 LAI-SVC-154`: a field fails its limit (lyrics over 8 KiB, an unknown field, a bad duration ...). */
export function musicInputRejected(error: ZodError | null, reason = 'invalid_input'): BadRequestException {
  return new BadRequestException({
    message: 'Some of the song settings are not valid. Check the lyrics, the style and the length.',
    details: { code: LAI_SVC_INPUT_REJECTED, reason, ...(error ? { issues: wireIssues(error) } : {}) },
  });
}

/** `413 LAI-SVC-154`: the sample is over 20 MiB, or a field is far over its limit. */
export function musicTooLarge(what: 'sample' | 'field'): HttpException {
  return new HttpException(
    {
      message: what === 'sample' ? 'The sample is too big. Use a file of 20 MB or less (at most 60 seconds).' : 'The lyrics or the style text are too long.',
      details: { code: LAI_SVC_INPUT_REJECTED, reason: what === 'sample' ? 'sample_too_large' : 'field_too_large' },
    },
    HttpStatus.PAYLOAD_TOO_LARGE,
  );
}

/** `415 LAI-SVC-154`: the sample is not wav, mp3, flac or ogg (by its bytes, not its name). */
export function musicSampleType(): HttpException {
  return new HttpException(
    {
      message: 'The sample must be a WAV, MP3, FLAC or OGG audio file.',
      details: { code: LAI_SVC_INPUT_REJECTED, reason: 'sample_type' },
    },
    HttpStatus.UNSUPPORTED_MEDIA_TYPE,
  );
}

/** `400`: the body is not multipart/form-data (or the server has no multipart reader). */
export function musicNotMultipart(): BadRequestException {
  return new BadRequestException({
    message: 'Send the song settings as a form (multipart/form-data).',
    details: { code: LAI_SVC_INPUT_REJECTED, reason: 'not_multipart' },
  });
}

/** `429 LAI-SVC-153`: the person already has too many songs waiting, or asked for too many this hour. */
export function musicRateLimited(reason: 'too_many_active' | 'too_many_per_hour', retryAfterS: number): HttpException {
  return new HttpException(
    {
      message:
        reason === 'too_many_active'
          ? 'You already have songs waiting. Wait for one to finish, then try again.'
          : 'You made many songs in the last hour. Wait a little, then try again.',
      details: { code: LAI_SVC_QUEUE_FULL, reason, retryAfterS },
    },
    HttpStatus.TOO_MANY_REQUESTS,
  );
}

/** `503`: object storage is not set up, so the sample and the songs have nowhere to go. */
export function musicStorageUnavailable(reason: 'not_configured' | 'not_https' | 'failed'): ServiceUnavailableException {
  const message =
    reason === 'not_configured'
      ? 'File storage is not set up yet. An administrator sets it up in Settings, Storage.'
      : reason === 'not_https'
        ? 'File storage must use an https address so the computers can reach it safely. An administrator fixes it in Settings, Storage.'
        : 'File storage did not answer. Try again in a minute.';
  return new ServiceUnavailableException({ message, details: { reason: `storage_${reason}` } });
}

/** `404` for a request id nobody knows (or a non-uuid). */
export function unknownMusicRequest(): NotFoundException {
  return new NotFoundException({ message: 'No such music request', details: { reason: 'unknown_music_request' } });
}

/** `404` for a song id nobody knows, or a song that is not ready. */
export function unknownSong(): NotFoundException {
  return new NotFoundException({ message: 'No such song', details: { reason: 'unknown_song' } });
}

/** `400`: a delete without the typed confirmation (the song's id), the same rule as retiring a node. */
export function songDeleteNotConfirmed(): BadRequestException {
  return new BadRequestException({
    message: 'Confirm the delete: send { "confirm": "<song id>" }.',
    details: { reason: 'confirmation_required' },
  });
}

/** `409`: only a request still waiting for its node can be cancelled from the cloud. */
export function musicRequestNotCancellable(status: string): ConflictException {
  return new ConflictException({
    message:
      status === 'running'
        ? 'The computer is already making this song. It cannot be stopped from here.'
        : 'This request has already finished or was cancelled.',
    details: { reason: 'not_cancellable', status },
  });
}
