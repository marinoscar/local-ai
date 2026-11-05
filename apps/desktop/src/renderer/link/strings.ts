// Texts of the link dialog in both languages (docs/specs/desktop.md 4.8). Pure data: the page and its unit test import it.
import type { Lang } from '../../shared/types';

export interface Bilingual {
  en: string;
  es: string;
}

export const pick = (text: Bilingual, lang: Lang): string => text[lang];

export const STRINGS = {
  title: { en: 'Connect to the lab', es: 'Conectar con el laboratorio' },
  loading: { en: 'Asking the lab for a code...', es: 'Pidiendo un código al laboratorio...' },
  instructions: {
    en: 'On any phone or computer, open ai.marin.cr/activate and enter this code.',
    es: 'En cualquier teléfono o computadora, abre ai.marin.cr/activate e ingresa este código.',
  },
  codeLabel: { en: 'Your code', es: 'Tu código' },
  openBrowser: { en: 'Open the browser', es: 'Abrir el navegador' },
  waiting: { en: 'Waiting for you to approve it...', es: 'Esperando que lo apruebes...' },
  countdown: { en: 'The code works for {time} more.', es: 'El código sirve por {time} más.' },
  saving: { en: 'Saving the connection...', es: 'Guardando la conexión...' },
  connected: { en: 'Connected', es: 'Conectado' },
  connectedAs: { en: 'Connected as {name}.', es: 'Conectado como {name}.' },
  connectedBody: { en: 'The Lab tab shows the whole lab while this computer is online.', es: 'La pestaña Laboratorio muestra todo el laboratorio mientras esta computadora esté en línea.' },
  memoryOnly: {
    en: 'The lab key is kept only until you close the app, because this computer has no password keyring. (LAI-DSK-008)',
    es: 'La clave del laboratorio se guarda solo hasta que cierres la aplicación, porque esta computadora no tiene un llavero de contraseñas. (LAI-DSK-008)',
  },
  expired: { en: 'The code expired. Try again. (LAI-DSK-009)', es: 'El código venció. Inténtalo de nuevo. (LAI-DSK-009)' },
  denied: { en: 'The code was refused. Try again. (LAI-DSK-009)', es: 'El código fue rechazado. Inténtalo de nuevo. (LAI-DSK-009)' },
  failed: { en: 'The lab answered in a way this app does not understand. Try again later.', es: 'El laboratorio respondió de una forma que esta aplicación no entiende. Inténtalo más tarde.' },
  unreachable: {
    en: "Can't reach the lab right now. Check the internet connection and try again. This computer keeps working normally.",
    es: 'No se puede conectar con el laboratorio ahora. Revisa la conexión a internet e inténtalo de nuevo. Esta computadora sigue funcionando normalmente.',
  },
  cancelled: { en: 'The connection was cancelled.', es: 'Se canceló la conexión.' },
  notOnline: { en: 'Switch this computer to Online first.', es: 'Primero cambia esta computadora a modo En línea.' },
  alreadyLinked: { en: 'This computer is already connected to the lab.', es: 'Esta computadora ya está conectada con el laboratorio.' },
  unavailable: { en: "The lab can't be linked right now. Try again later.", es: 'No se puede conectar con el laboratorio ahora. Inténtalo más tarde.' },
  tryAgain: { en: 'Try again', es: 'Intentar de nuevo' },
  close: { en: 'Close', es: 'Cerrar' },
  cancel: { en: 'Cancel', es: 'Cancelar' },
} as const satisfies Record<string, Bilingual>;

export function fill(text: string, values: Readonly<Record<string, string>>): string {
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => values[name] ?? whole);
}

/** `14:32` for 872 seconds. */
export function clock(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}
