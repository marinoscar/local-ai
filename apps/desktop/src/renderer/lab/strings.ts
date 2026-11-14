// Texts of the Lab view in both languages (docs/specs/desktop.md 4.8). Pure data: the page and its unit test import it.
// The node cards themselves (`@localai/ui`) are English-only text; everything the desktop adds around them is here.
import type { Lang } from '../../shared/types';

export interface Bilingual {
  en: string;
  es: string;
}

export const pick = (text: Bilingual, lang: Lang): string => text[lang];

export const STRINGS = {
  title: { en: 'Lab', es: 'Laboratorio' },
  loading: { en: 'Loading the lab...', es: 'Cargando el laboratorio...' },
  connectedAs: { en: 'Connected as {name}', es: 'Conectado como {name}' },
  connected: { en: 'Connected to the lab', es: 'Conectado con el laboratorio' },
  summary: { en: '{nodes} computers · {online} online · {alerts} alerts', es: '{nodes} computadoras · {online} en línea · {alerts} alertas' },
  empty: { en: 'No computers in the lab yet.', es: 'Todavía no hay computadoras en el laboratorio.' },
  notConnected: {
    en: 'The Lab is available while this computer is online and connected to the lab.',
    es: 'El laboratorio está disponible mientras esta computadora esté en línea y conectada con el laboratorio.',
  },
  // desktop.md 4.8: the unreachable banner, with the time of the cached data.
  unreachable: {
    en: "Can't reach the lab right now. Showing information from {time}. This computer keeps working normally.",
    es: 'No se puede conectar con el laboratorio. Mostrando información de las {time}. Esta computadora sigue funcionando normalmente.',
  },
  unreachableNoData: {
    en: "Can't reach the lab right now and there is no saved information yet. This computer keeps working normally.",
    es: 'No se puede conectar con el laboratorio y todavía no hay información guardada. Esta computadora sigue funcionando normalmente.',
  },
  retrying: { en: 'Trying again by itself.', es: 'Se vuelve a intentar solo.' },
  tryNow: { en: 'Try now', es: 'Intentar ahora' },
  forbidden: { en: "Your lab role can't see the lab.", es: 'Tu rol en el laboratorio no puede ver el laboratorio.' },
  removed: {
    en: 'The lab connection was removed. Press "Connect to the lab" in the menu to connect again. (LAI-DSK-011)',
    es: 'Se quitó la conexión con el laboratorio. Pulsa "Conectar con el laboratorio" en el menú para conectar otra vez. (LAI-DSK-011)',
  },
  unexpected: { en: 'The lab answered in a way this app does not understand.', es: 'El laboratorio respondió de una forma que esta aplicación no entiende.' },

  // Card buttons.
  openInBrowser: { en: 'Open in browser', es: 'Abrir en el navegador' },
  refreshStatus: { en: 'Refresh status', es: 'Actualizar estado' },
  syncNow: { en: 'Sync now', es: 'Sincronizar ahora' },
  verify: { en: 'Verify', es: 'Verificar' },
  restartService: { en: 'Restart service', es: 'Reiniciar servicio' },
  models: { en: 'Models', es: 'Modelos' },
  hideModels: { en: 'Hide models', es: 'Ocultar modelos' },
  load: { en: 'Load', es: 'Cargar' },
  unload: { en: 'Unload', es: 'Descargar' },
  setDefault: { en: 'Set default', es: 'Predeterminado' },
  isDefault: { en: 'default', es: 'predeterminado' },
  modelsLoading: { en: 'Reading the models...', es: 'Leyendo los modelos...' },
  modelsNone: { en: 'This computer has not reported any models yet.', es: 'Esta computadora todavía no ha informado de modelos.' },
  modelsError: { en: "Couldn't read the models.", es: 'No se pudieron leer los modelos.' },
  notReady: { en: 'Not finished enrolling', es: 'No terminó de registrarse' },

  // A command on its way (desktop.md 4.8: polled every 2 s).
  sending: { en: 'Sending...', es: 'Enviando...' },
  waiting: { en: 'Waiting…', es: 'Esperando…' },
  running: { en: 'Running…', es: 'En curso…' },
  done: { en: 'Done', es: 'Listo' },
  failed: { en: 'Failed', es: 'Falló' },
  expired: {
    en: 'The computer did not pick it up in time. (LAI-CLD-030)',
    es: 'La computadora no lo recogió a tiempo. (LAI-CLD-030)',
  },
  cancelled: { en: 'Cancelled', es: 'Cancelado' },

  // Refusals of a command (cloud-app.md 3.9).
  refusedForbidden: { en: "Your lab role can't do this.", es: 'Tu rol en el laboratorio no puede hacer esto.' },
  refusedOffline: {
    en: 'This computer is in Offline mode. Only someone at it can switch it back. (LAI-CLD-034)',
    es: 'Esa computadora está en modo Sin conexión. Solo alguien frente a ella puede volver a cambiarlo. (LAI-CLD-034)',
  },
  refusedSurvival: {
    en: 'This computer is in Survival mode. Only someone at it can switch it back. (LAI-CLD-034)',
    es: 'Esa computadora está en modo Supervivencia. Solo alguien frente a ella puede volver a cambiarlo. (LAI-CLD-034)',
  },
  refusedNotEnrolled: {
    en: 'This computer has been retired or has not finished enrolling. (LAI-CLD-035)',
    es: 'Esta computadora fue retirada o no terminó de registrarse. (LAI-CLD-035)',
  },
  refusedModel: { en: 'This model is not on that computer yet. Download it first.', es: 'Ese modelo todavía no está en esa computadora. Descárgalo primero.' },
  refusedGeneric: { en: 'The lab did not accept this. Try again later.', es: 'El laboratorio no lo aceptó. Inténtalo más tarde.' },
  refusedUnreachable: {
    en: "Can't reach the lab right now. Nothing was sent. (LAI-DSK-010)",
    es: 'No se puede conectar con el laboratorio. No se envió nada. (LAI-DSK-010)',
  },

  // Workloads and music (issue #515).
  workloads: { en: 'Workloads', es: 'Cargas de trabajo' },
  hideWorkloads: { en: 'Hide workloads', es: 'Ocultar cargas de trabajo' },
  workloadsLoading: { en: 'Reading the workloads...', es: 'Leyendo las cargas de trabajo...' },
  workloadsNone: { en: 'This computer has not reported any workloads yet.', es: 'Esta computadora todavía no ha informado de cargas de trabajo.' },
  workloadsError: { en: "Couldn't read the workloads.", es: 'No se pudieron leer las cargas de trabajo.' },
  workloadsHelp: {
    en: 'A workload loads the models for one job and unloads the others. Choose one:',
    es: 'Una carga de trabajo carga los modelos para una tarea y descarga los demás. Elige una:',
  },
  workloadActive: { en: 'in use', es: 'en uso' },
  workloadDoesNotFit: { en: 'too big for this computer', es: 'demasiado grande para esta computadora' },
  workloadMissing: { en: '{count} models not downloaded yet', es: '{count} modelos aún sin descargar' },
  sent: { en: 'Sent. The computer picks it up in a moment.', es: 'Enviado. La computadora lo recoge en un momento.' },
  makeMusic: { en: 'Make music on this node', es: 'Hacer música en esta computadora' },
  musicTitle: { en: 'Make music on {node}', es: 'Hacer música en {node}' },
  musicIntro: {
    en: 'Write the lyrics or describe the style. {node} makes the song and the lab keeps it.',
    es: 'Escribe la letra o describe el estilo. {node} hace la canción y el laboratorio la guarda.',
  },
  musicNotActive: {
    en: 'This computer is not on the Music workload. Switch it to Music under "Workloads" first.',
    es: 'Esta computadora no está en la carga de trabajo Música. Cámbiala a Música en "Cargas de trabajo" primero.',
  },
  lyrics: { en: 'Lyrics', es: 'Letra' },
  tags: { en: 'Style (for example: folk, guitar, calm)', es: 'Estilo (por ejemplo: folk, guitarra, tranquilo)' },
  duration: { en: 'Length in seconds (10 to 240)', es: 'Duración en segundos (10 a 240)' },
  language: { en: 'Language of the lyrics', es: 'Idioma de la letra' },
  english: { en: 'English', es: 'Inglés' },
  spanish: { en: 'Spanish', es: 'Español' },
  variations: { en: 'How many versions (1 to 4)', es: 'Cuántas versiones (1 a 4)' },
  musicNeedsText: { en: 'Write the lyrics or the style first.', es: 'Escribe primero la letra o el estilo.' },
  musicTooLong: { en: 'The lyrics are too long (8 KB at most).', es: 'La letra es demasiado larga (8 KB como máximo).' },
  musicTagsTooLong: { en: 'The style is too long (512 characters at most).', es: 'El estilo es demasiado largo (512 caracteres como máximo).' },
  makeIt: { en: 'Make the song', es: 'Hacer la canción' },
  musicSent: { en: 'Sent. The lab is passing the request to the computer.', es: 'Enviado. El laboratorio pasa la petición a la computadora.' },
  musicQueued: { en: 'Waiting for the computer…', es: 'Esperando a la computadora…' },
  musicRunning: { en: 'Making the song… This can take a few minutes.', es: 'Haciendo la canción… Puede tardar unos minutos.' },
  musicDone: { en: 'The song is ready.', es: 'La canción está lista.' },
  musicCancelled: { en: 'The song was cancelled.', es: 'Se canceló la canción.' },
  songs: { en: 'Latest songs from this computer', es: 'Últimas canciones de esta computadora' },
  songsLoading: { en: 'Reading the songs...', es: 'Leyendo las canciones...' },
  songsNone: { en: 'No songs yet.', es: 'Todavía no hay canciones.' },
  songsError: { en: "Couldn't read the songs.", es: 'No se pudieron leer las canciones.' },
  songsListen: {
    en: 'To listen or download, open Music on ai.marin.cr in your web browser.',
    es: 'Para escuchar o descargar, abre Música en ai.marin.cr en tu navegador.',
  },
  untitled: { en: 'Untitled song', es: 'Canción sin título' },
  close: { en: 'Close', es: 'Cerrar' },
  refusedWorkloadUnknown: { en: 'That computer does not have this workload. (LAI-LLM-050)', es: 'Esa computadora no tiene esta carga de trabajo. (LAI-LLM-050)' },
  refusedWorkloadFit: {
    en: 'This workload does not fit in that computer\'s graphics card. (LAI-LLM-051)',
    es: 'Esta carga de trabajo no cabe en la tarjeta gráfica de esa computadora. (LAI-LLM-051)',
  },
  refusedWorkloadSurvival: {
    en: 'That computer is in Survival mode, so its workload stays as it is. (LAI-LLM-053)',
    es: 'Esa computadora está en modo Supervivencia, así que su carga de trabajo no cambia. (LAI-LLM-053)',
  },
  refusedMusicNotLoaded: {
    en: 'Music is not loaded on that computer. Switch it to the Music workload first. (LAI-SVC-151)',
    es: 'La música no está cargada en esa computadora. Cámbiala primero a la carga de trabajo Música. (LAI-SVC-151)',
  },
  refusedMusicBusy: { en: 'That computer is already making songs. Try again in a few minutes. (LAI-SVC-153)', es: 'Esa computadora ya está haciendo canciones. Inténtalo en unos minutos. (LAI-SVC-153)' },
  refusedMusicInput: { en: 'The request was not accepted. Check the lyrics, style and length. (LAI-SVC-154)', es: 'No se aceptó la petición. Revisa la letra, el estilo y la duración. (LAI-SVC-154)' },

  // Restart service confirmation.
  restartTitle: { en: 'Restart {service} on {node}?', es: '¿Reiniciar {service} en {node}?' },
  restartBody: {
    en: 'Whatever uses that service on the computer stops for a moment.',
    es: 'Lo que use ese servicio en la computadora se detiene un momento.',
  },
  restart: { en: 'Restart', es: 'Reiniciar' },
  cancel: { en: 'Cancel', es: 'Cancelar' },
} as const satisfies Record<string, Bilingual>;

export function fill(text: string, values: Readonly<Record<string, string>>): string {
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => values[name] ?? whole);
}
