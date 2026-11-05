// Texts of the top bar in both languages (docs/specs/desktop.md 3.4). Pure data: the page and its unit test import it.
import type { Lang } from '../../shared/types';

export interface Bilingual {
  en: string;
  es: string;
}

export const pick = (text: Bilingual, lang: Lang): string => text[lang];

export const STRINGS = {
  tabLocal: { en: 'This computer', es: 'Esta computadora' },
  tabLab: { en: 'Lab', es: 'Laboratorio' },
  chat: { en: 'Chat', es: 'Chat' },
  library: { en: 'Library', es: 'Biblioteca' },
  maps: { en: 'Maps', es: 'Mapas' },
  copyKey: { en: 'Copy API key', es: 'Copiar clave de API' },
  copyKeyShort: { en: 'API key', es: 'Clave API' },
  menu: { en: 'Menu', es: 'Menú' },
  pillReady: { en: 'Ready', es: 'Listo' },
  pillStarting: { en: 'Starting', es: 'Iniciando' },
  pillDown: { en: 'Not working', es: 'No funciona' },
  pillSignIn: { en: 'Sign-in needed', es: 'Falta iniciar sesión' },
  pillAttention: { en: 'Needs attention', es: 'Necesita atención' },
  modeOnline: { en: 'Online', es: 'En línea' },
  modeOffline: { en: 'Offline', es: 'Sin conexión' },
  modeSurvival: { en: 'Survival', es: 'Supervivencia' },
  notAvailable: { en: 'Not available yet.', es: 'Todavía no está disponible.' },
  copyFailed: { en: 'The API key could not be copied.', es: 'No se pudo copiar la clave de API.' },
  tabs: { en: 'Views', es: 'Vistas' },
  // The survival strip (desktop.md 4.7)
  survivalMode: { en: 'Survival mode', es: 'Modo supervivencia' },
  survivalPortal: { en: 'Survival Portal', es: 'Portal de supervivencia' },
  language: { en: 'Language', es: 'Idioma' },
  exitFullScreen: { en: 'Exit full screen', es: 'Salir de pantalla completa' },
  enterFullScreen: { en: 'Full screen', es: 'Pantalla completa' },
  backToDashboard: { en: 'Back to dashboard', es: 'Volver al panel' },
} as const satisfies Record<string, Bilingual>;

/** "API key copied. It will be cleared from the clipboard in 60 seconds." (desktop.md 4.6) */
export function copiedText(seconds: number, lang: Lang): string {
  return lang === 'es'
    ? `Clave copiada. Se borrará del portapapeles en ${seconds} segundos.`
    : `API key copied. It will be cleared from the clipboard in ${seconds} seconds.`;
}

/** "The API key could not be copied." plus the doctor code when the failure has one (every visible error carries its code). */
export function copyFailedText(code: string, lang: Lang): string {
  const text = pick(STRINGS.copyFailed, lang);
  return code.startsWith('LAI-') ? `${text} (${code})` : text;
}
