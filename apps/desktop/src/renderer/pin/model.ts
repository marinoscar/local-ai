// The PIN window's texts and decisions (docs/specs/desktop.md 4.6). Pure data and functions: the page and its unit test import it.
import type { Lang, PinResult } from '../../shared/types';

export type PinPurpose = 'leave-survival' | 'rotate-key' | 'confirm-ensure';

interface Bilingual {
  en: string;
  es: string;
}

export const PIN_LENGTH = { min: 4, max: 8 } as const;

export const STRINGS = {
  titleLeaveSurvival: { en: 'Leave survival mode', es: 'Salir del modo supervivencia' },
  titleRotateKey: { en: 'Rotate the API key', es: 'Cambiar la clave de API' },
  promptLeaveSurvival: { en: 'Enter the administrator PIN to leave survival mode.', es: 'Escribe el PIN de administrador para salir del modo supervivencia.' },
  promptRotateKey: { en: 'Enter the administrator PIN to create a new API key.', es: 'Escribe el PIN de administrador para crear una clave de API nueva.' },
  titleConfirmEnsure: { en: 'Approve the install', es: 'Aprobar la instalación' },
  promptConfirmEnsure: { en: 'Enter the administrator PIN to approve compiling or restarting a running service.', es: 'Escribe el PIN de administrador para aprobar la compilación o el reinicio de un servicio en marcha.' },
  label: { en: 'PIN (4 to 8 digits)', es: 'PIN (de 4 a 8 dígitos)' },
  ok: { en: 'OK', es: 'Aceptar' },
  cancel: { en: 'Cancel', es: 'Cancelar' },
  close: { en: 'Close', es: 'Cerrar' },
  checking: { en: 'Checking…', es: 'Comprobando…' },
  fix: { en: 'Fix', es: 'Solución' },
  wrongNoCount: { en: 'Wrong PIN.', es: 'PIN incorrecto.' },
  wrongTriesOne: { en: 'Wrong PIN. 1 try left.', es: 'PIN incorrecto. Queda 1 intento.' },
  wrongTries: { en: 'Wrong PIN. {n} tries left.', es: 'PIN incorrecto. Quedan {n} intentos.' },
  wrongFix: { en: 'Try again. After 5 wrong tries you must wait 5 minutes.', es: 'Inténtalo de nuevo. Después de 5 intentos fallidos debes esperar 5 minutos.' },
  lockedOne: { en: 'Too many tries. Wait 1 minute.', es: 'Demasiados intentos. Espera 1 minuto.' },
  locked: { en: 'Too many tries. Wait {n} minutes.', es: 'Demasiados intentos. Espera {n} minutos.' },
  lockedFix: { en: 'Forgotten PIN: run in a terminal: sudo aictl pin set', es: 'PIN olvidado: ejecuta en una terminal: sudo aictl pin set' },
  noPin: { en: 'No administrator PIN has been set.', es: 'No se ha definido un PIN de administrador.' },
  noPinFix: { en: 'Run in a terminal: sudo aictl pin set', es: 'Ejecuta en una terminal: sudo aictl pin set' },
  other: { en: 'The PIN could not be checked.', es: 'No se pudo comprobar el PIN.' },
  otherFix: { en: 'Close this window and try again.', es: 'Cierra esta ventana e inténtalo de nuevo.' },
} as const satisfies Record<string, Bilingual>;

export const pick = (text: Bilingual, lang: Lang): string => text[lang];
const fill = (text: string, n: number): string => text.replace('{n}', String(n));

const PURPOSES: readonly PinPurpose[] = ['leave-survival', 'rotate-key', 'confirm-ensure'];

/** The title and prompt of a purpose. */
export function textsFor(purpose: PinPurpose): { title: Bilingual; prompt: Bilingual } {
  switch (purpose) {
    case 'rotate-key':
      return { title: STRINGS.titleRotateKey, prompt: STRINGS.promptRotateKey };
    case 'confirm-ensure':
      return { title: STRINGS.titleConfirmEnsure, prompt: STRINGS.promptConfirmEnsure };
    default:
      return { title: STRINGS.titleLeaveSurvival, prompt: STRINGS.promptLeaveSurvival };
  }
}

/** `?purpose=rotate-key&lang=es`. Anything else falls back to the safest reading (leave survival, English). */
export function parseQuery(search: string): { purpose: PinPurpose; lang: Lang } {
  const params = new URLSearchParams(search);
  const asked = params.get('purpose');
  return { purpose: PURPOSES.find((p) => p === asked) ?? 'leave-survival', lang: params.get('lang') === 'es' ? 'es' : 'en' };
}

/** Digits only, at most 8: what the field keeps of whatever was typed or pasted. */
export function sanitizePin(raw: string): string {
  return raw.replace(/\D/g, '').slice(0, PIN_LENGTH.max);
}

export function canSubmit(pin: string): boolean {
  return pin.length >= PIN_LENGTH.min && pin.length <= PIN_LENGTH.max && /^\d+$/.test(pin);
}

export interface Feedback {
  text: string;
  fix: string;
  code?: string;
  /** No further try is possible from this window (locked, or no PIN set): the field is disabled. */
  final: boolean;
}

/** What the window says about a refusal of `POST /pin/verify`; null for a success. */
export function feedbackFor(result: PinResult, lang: Lang): Feedback | null {
  if (result.ok) return null;
  switch (result.code) {
    case 'LAI-SEC-003': {
      const n = result.remainingAttempts;
      const text = n === undefined ? pick(STRINGS.wrongNoCount, lang) : n === 1 ? pick(STRINGS.wrongTriesOne, lang) : fill(pick(STRINGS.wrongTries, lang), n);
      return { text, fix: pick(STRINGS.wrongFix, lang), code: result.code, final: false };
    }
    case 'LAI-SEC-004': {
      const minutes = Math.max(1, Math.ceil((result.retryAfterS ?? 300) / 60));
      return { text: minutes === 1 ? pick(STRINGS.lockedOne, lang) : fill(pick(STRINGS.locked, lang), minutes), fix: pick(STRINGS.lockedFix, lang), code: result.code, final: true };
    }
    case 'LAI-SEC-005':
      return { text: pick(STRINGS.noPin, lang), fix: pick(STRINGS.noPinFix, lang), code: result.code, final: true };
    case 'invalid_format':
      return { text: pick(STRINGS.wrongNoCount, lang), fix: pick(STRINGS.wrongFix, lang), final: false };
    default:
      return { text: pick(STRINGS.other, lang), fix: result.fix ?? pick(STRINGS.otherFix, lang), ...(result.code?.startsWith('LAI-') ? { code: result.code } : {}), final: false };
  }
}
