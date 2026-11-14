// The words of the music page in both languages (ADR-0009: every screen in English and Spanish). Pure.
import { ApiError } from '@marinoscar/platform-web/core';

import { plainError } from '../doctorFix';
import type { MusicReadiness, MusicRequest, MusicStage } from './musicApi';

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
