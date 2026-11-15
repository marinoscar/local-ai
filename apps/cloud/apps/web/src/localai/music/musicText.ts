// The words of the music page in both languages (ADR-0009: every screen in English and Spanish). Pure.
import { ApiError } from '@marinoscar/platform-web/core';

import { plainError } from '../doctorFix';
import type { MusicAiReason, MusicAssist, MusicReadiness, MusicRequest, MusicStage } from './musicApi';

type T = (en: string, es: string) => string;

export function readinessText(readiness: MusicReadiness, t: T): string {
  switch (readiness) {
    case 'ready':
      return t('Music ready', 'Música lista');
    case 'can_load':
      return t('Can load music', 'Puede cargar música');
    case 'too_little_vram':
      return t('Too little VRAM', 'Memoria de video insuficiente');
    case 'offline':
      return t('Offline', 'Sin conexión');
    case 'no_music':
      return t('No music here', 'Sin música aquí');
  }
}

/** The colour of a readiness chip; never the only signal (the chip carries the words). */
export function readinessColor(readiness: MusicReadiness): 'success' | 'info' | 'warning' | 'default' {
  return readiness === 'ready' ? 'success' : readiness === 'can_load' ? 'info' : readiness === 'too_little_vram' ? 'warning' : 'default';
}

/** The one next thing to do for the chosen node (state first). */
export function nextActionText(readiness: MusicReadiness, nodeName: string, t: T): string {
  switch (readiness) {
    case 'ready':
      return t(`${nodeName} is ready. Write your song below.`, `${nodeName} está lista. Escriba su canción abajo.`);
    case 'can_load':
      return t(`Switch ${nodeName} to Music first.`, `Primero cambie ${nodeName} a Música.`);
    case 'too_little_vram':
      return t(`The graphics card of ${nodeName} is too small for music. Choose another computer.`, `La tarjeta gráfica de ${nodeName} es muy pequeña para música. Elija otra computadora.`);
    case 'offline':
      return t(`${nodeName} is not online. Turn it on, or choose another computer.`, `${nodeName} no está en línea. Enciéndala o elija otra computadora.`);
    case 'no_music':
      return t(`Music is not installed on ${nodeName}. Choose another computer.`, `La música no está instalada en ${nodeName}. Elija otra computadora.`);
  }
}

export function stageText(request: Pick<MusicRequest, 'stage' | 'nodeName'>, t: T): string {
  const stage: MusicStage = request.stage;
  switch (stage) {
    case 'queued':
      return t(`Waiting for ${request.nodeName}`, `Esperando a ${request.nodeName}`);
    case 'generating':
      return t(`${request.nodeName} is making the song…`, `${request.nodeName} está creando la canción…`);
    case 'done':
      return t('Done', 'Lista');
    case 'failed':
      return t('Failed', 'Falló');
    case 'expired':
      return t('Not picked up in time', 'No se recogió a tiempo');
    case 'cancelled':
      return t('Cancelled', 'Cancelada');
  }
}

/** The error of a request, with its doctor code: "Music is not loaded … (LAI-SVC-151)". */
export function requestErrorText(error: MusicRequest['error']): string | null {
  if (!error) return null;
  const plain = plainError(error);
  const text = plain.meaning ? `${plain.meaning}. ${plain.fix}` : plain.fix;
  return plain.code ? `${text} (${plain.code})` : text;
}

/** A refusal of the API as a person reads it, with its doctor code when it has one. */
export function refusalText(error: unknown, t: T): string {
  if (error instanceof ApiError) {
    const details = (error.details ?? {}) as { code?: unknown };
    const code = typeof details.code === 'string' ? details.code : null;
    if (error.status === 403) return t('You do not have permission to do this.', 'No tiene permiso para hacer esto.');
    const message = error.message || t('The cloud refused the request.', 'La nube rechazó la solicitud.');
    return code ? `${message} (${code})` : message;
  }
  return t('Could not reach the cloud. Check your connection and try again.', 'No se pudo llegar a la nube. Revise su conexión e intente otra vez.');
}

export const formatDuration = (seconds: number | null): string => {
  if (seconds === null || !Number.isFinite(seconds)) return '';
  const whole = Math.round(seconds);
  return `${Math.floor(whole / 60)}:${String(whole % 60).padStart(2, '0')}`;
};

/**
 * Why music AI is off for this person, and the fix, in one line (#570). The order is the API's: the AI itself, then
 * "Use AI for music", then a model. The settings names are the ones the (English) administration pages show.
 */
export function musicAiOffText(assist: MusicAssist | null, t: T): string {
  const reason: MusicAiReason = assist?.ai.enabled === false ? assist.ai.reason : 'ai_off';
  switch (reason) {
    case 'music_ai_off':
      return t(
        'Music AI is off: Use AI for music is turned off. An administrator turns on Use AI for music in Settings → AI.',
        'La IA para música está apagada: "Use AI for music" está apagado. Un administrador lo enciende en Ajustes (Settings) → AI.',
      );
    case 'no_model':
      return t(
        'Music AI is off: no AI model is set up for you. An administrator adds a provider and model in Settings → AI.',
        'La IA para música está apagada: no hay un modelo de IA para usted. Un administrador agrega un proveedor y un modelo en Ajustes (Settings) → AI.',
      );
    case 'ai_off':
      return t(
        'Music AI is off: the AI is turned off on this cloud. An administrator turns it on in Settings → AI.',
        'La IA para música está apagada: la IA está apagada en esta nube. Un administrador la enciende en Ajustes (Settings) → AI.',
      );
  }
}

/** The one-line hint under the lyrics box in Automatic mode: what Make the song will do with the box as it is. */
export function autoLyricsHint(assist: MusicAssist | null, lyrics: string, t: T): string {
  if (lyrics.trim() !== '') return t('Your lyrics are used.', 'Se usa su letra.');
  if (assist?.ai.enabled === true) {
    return t('Leave it empty and the AI writes the lyrics when you press Make the song.', 'Déjela vacía y la IA escribe la letra cuando pulse Crear la canción.');
  }
  return t('Leave it empty for a song with no vocals.', 'Déjela vacía para una canción sin voz.');
}

/** A size as people read it: 512 B, 3.4 KB, 1.2 MB. */
export const formatBytes = (bytes: number): string => {
  if (bytes < 1000) return `${bytes} B`;
  if (bytes < 1_000_000) return `${(bytes / 1000).toFixed(1)} KB`;
  return `${(bytes / 1_000_000).toFixed(1)} MB`;
};
