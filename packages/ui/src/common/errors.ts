// The error shape every transport of the shared creative components (music, workloads) reports.
//
// A transport turns whatever its API answers into a TransportError, so the components can always show a doctor code
// (AGENTS.md rule 4: every error a user can see carries a code and a one-sentence fix). The agent sends the fix in the
// UI language; when a transport has none, the component falls back to codeFix() below.
import { pick, type Lang } from '../lang';

/** The failure of a transport call. `code` is a doctor code (`LAI-XXX-NNN`); `fix` may be empty. */
export interface TransportErrorInfo {
  code: string;
  message: string;
  fix?: string;
  /** HTTP status when there was an answer; absent for a network failure. */
  status?: number;
}

/** TransportError is what a transport throws. Components also accept any Error carrying `code`. */
export class TransportError extends Error implements TransportErrorInfo {
  readonly code: string;
  readonly fix: string;
  readonly status?: number;
  constructor(info: TransportErrorInfo) {
    super(info.message);
    this.name = 'TransportError';
    this.code = info.code;
    this.fix = info.fix ?? '';
    this.status = info.status;
  }
}

const CODE_RE = /^LAI-[A-Z]{2,4}-\d{3}$/;

/** True when `code` looks like a doctor code. */
export function isDoctorCode(code: unknown): code is string {
  return typeof code === 'string' && CODE_RE.test(code);
}

/**
 * toErrorInfo normalizes anything a transport threw. An error without a doctor code gets `fallbackCode`: the code of
 * the action that failed, so the person still has something to look up.
 */
export function toErrorInfo(e: unknown, fallbackCode: string): TransportErrorInfo {
  if (e && typeof e === 'object') {
    const o = e as { code?: unknown; message?: unknown; fix?: unknown; status?: unknown };
    return {
      code: isDoctorCode(o.code) ? o.code : fallbackCode,
      message: typeof o.message === 'string' ? o.message : '',
      fix: typeof o.fix === 'string' ? o.fix : '',
      status: typeof o.status === 'number' ? o.status : undefined,
    };
  }
  return { code: fallbackCode, message: typeof e === 'string' ? e : '' };
}

// The codes these components can show (docs/specs/music.md section 8, llama-router.md section 8), with a short
// meaning and a one-sentence fix in English and Spanish. The agent's own text wins; this is the fallback.
const CODES: Record<string, { en: [string, string]; es: [string, string] }> = {
  'LAI-SVC-150': {
    en: ['Music is not installed on this computer.', 'Connect the AI-ARK drive and run "Install what is missing", or ask the lab owner to install music.'],
    es: ['La música no está instalada en esta computadora.', 'Conecte el disco AI-ARK y use "Instalar lo que falta", o pida al responsable del laboratorio que instale la música.'],
  },
  'LAI-SVC-151': {
    en: ['Music is not loaded.', 'Press "Switch to Music" and wait until it says Music is ready.'],
    es: ['La música no está cargada.', 'Pulse "Cambiar a Música" y espere a que diga que la música está lista.'],
  },
  'LAI-SVC-152': {
    en: ['The music engine stopped while making the song.', 'Press Generate again; if it happens again, switch to Music again first.'],
    es: ['El motor de música se detuvo mientras hacía la canción.', 'Pulse Generar otra vez; si se repite, cambie de nuevo a Música primero.'],
  },
  'LAI-SVC-153': {
    en: ['Too many songs are waiting.', 'Wait for a song to finish, then press Generate again.'],
    es: ['Hay demasiadas canciones esperando.', 'Espere a que termine una canción y pulse Generar otra vez.'],
  },
  'LAI-SVC-154': {
    en: ['Something in the form or the sample was not accepted.', 'Check the fields marked in red; a sample must be wav, mp3, flac or ogg, at most 20 MB and 60 seconds.'],
    es: ['Algo del formulario o de la muestra no se aceptó.', 'Revise los campos marcados en rojo; la muestra debe ser wav, mp3, flac u ogg, de 20 MB y 60 segundos como máximo.'],
  },
  'LAI-SVC-155': {
    en: ['There is not enough disk space to save the song.', 'Delete old songs you do not need, then try again.'],
    es: ['No hay espacio suficiente en el disco para guardar la canción.', 'Borre canciones viejas que no necesite y vuelva a intentarlo.'],
  },
  'LAI-SVC-156': {
    en: ['The sample could not be downloaded or did not match.', 'Upload the sample again and send the request again.'],
    es: ['La muestra no se pudo descargar o no coincidía.', 'Suba la muestra otra vez y envíe la solicitud de nuevo.'],
  },
  'LAI-SVC-157': {
    en: ['The song could not be uploaded.', 'The song is kept on the computer; try the request again later.'],
    es: ['La canción no se pudo subir.', 'La canción queda guardada en la computadora; repita la solicitud más tarde.'],
  },
  'LAI-SVC-158': {
    en: ['The song was cancelled.', 'Press Generate when you want to make it again.'],
    es: ['Se canceló la canción.', 'Pulse Generar cuando quiera hacerla de nuevo.'],
  },
  'LAI-SVC-159': {
    en: ['The graphics driver is too old for music.', 'Ask the lab owner to update the graphics driver of this computer.'],
    es: ['El controlador gráfico es demasiado viejo para la música.', 'Pida al responsable del laboratorio que actualice el controlador gráfico de esta computadora.'],
  },
  'LAI-SVC-116': {
    en: ['The AI service is not answering.', 'Wait a minute: it tries again by itself. If it stays, restart the computer.'],
    es: ['El servicio de IA no responde.', 'Espere un minuto: lo intenta de nuevo solo. Si sigue así, reinicie la computadora.'],
  },
  'LAI-LLM-041': {
    en: ['The engine did not become ready in time.', 'Try again; if it fails twice, restart the computer.'],
    es: ['El motor no estuvo listo a tiempo.', 'Inténtelo de nuevo; si falla dos veces, reinicie la computadora.'],
  },
  'LAI-LLM-043': {
    en: ["This model's engine is not installed here.", 'Connect the AI-ARK drive and run "Install what is missing".'],
    es: ['El motor de este modelo no está instalado aquí.', 'Conecte el disco AI-ARK y use "Instalar lo que falta".'],
  },
  'LAI-LLM-050': {
    en: ['This computer does not know that task.', 'Choose one of the buttons shown on this screen.'],
    es: ['Esta computadora no conoce esa tarea.', 'Elija uno de los botones de esta pantalla.'],
  },
  'LAI-LLM-051': {
    en: ['This task does not fit in this computer\'s graphics memory.', 'Use it on a computer with a bigger graphics card.'],
    es: ['Esta tarea no cabe en la memoria gráfica de esta computadora.', 'Úsela en una computadora con una tarjeta gráfica más grande.'],
  },
  'LAI-LLM-052': {
    en: ['The switch failed; the previous task was put back.', 'Try again in a minute; if it fails again, restart the computer.'],
    es: ['El cambio falló; se restauró la tarea anterior.', 'Inténtelo de nuevo en un minuto; si vuelve a fallar, reinicie la computadora.'],
  },
  'LAI-LLM-053': {
    en: ['Survival mode keeps the emergency brain running.', 'Leave survival mode first, then switch.'],
    es: ['El modo supervivencia mantiene el cerebro de emergencia.', 'Salga primero del modo supervivencia y luego cambie.'],
  },
  'LAI-AGT-022': {
    en: ['Another operation is already running.', 'Wait for it to finish, then try again.'],
    es: ['Ya hay otra operación en curso.', 'Espere a que termine y vuelva a intentarlo.'],
  },
  'LAI-SEC-002': {
    en: ['This needs the admin PIN.', 'Enter the admin PIN, then try again.'],
    es: ['Esto necesita el PIN de administrador.', 'Escriba el PIN de administrador y vuelva a intentarlo.'],
  },
};

/** The fallback meaning of a code, or '' when this package does not know it. */
export function codeMeaning(code: string, lang: Lang): string {
  const c = CODES[code];
  return c ? pick(lang, c.en[0], c.es[0]) : '';
}

/** The fallback one-sentence fix of a code, or '' when this package does not know it. */
export function codeFix(code: string, lang: Lang): string {
  const c = CODES[code];
  return c ? pick(lang, c.en[1], c.es[1]) : '';
}

/** The codes with a fallback text (tests check both languages). */
export const KNOWN_CODES: readonly string[] = Object.keys(CODES);
