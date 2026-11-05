// English and Spanish strings for the error pages, the doctor checks of `--diagnose` and main-process dialogs.
// Pure data and functions, no Node or Electron import: the error page (src/renderer/error) imports this file too.
// Codes are registered in docs/specs/doctor-codes.md (LAI-DSK); the texts follow desktop.md sections 4.2, 5 and 8.
export const MODULE = 'i18n' as const;

export type Lang = 'en' | 'es';
export type LangSetting = Lang | 'auto';
export type Severity = 'critical' | 'error' | 'warning' | 'info';

export interface Bilingual {
  en: string;
  es: string;
}

/** `auto` follows the operating system: any `es` locale (`es`, `es-CR`, `es_MX.UTF-8`) is Spanish, everything else English. */
export function resolveLang(setting: LangSetting | string | undefined, osLocale: string | undefined): Lang {
  if (setting === 'en' || setting === 'es') return setting;
  return /^es([_-]|\.|$)/i.test((osLocale ?? '').trim()) ? 'es' : 'en';
}

export const pick = (text: Bilingual, lang: Lang): string => text[lang];

// ---------------------------------------------------------------------------------------------------------------------
// Error pages
// ---------------------------------------------------------------------------------------------------------------------

/** The codes this app can show as a page or in `--diagnose`. All are registered in doctor-codes.md. */
export const ERROR_CODES = ['LAI-DSK-001', 'LAI-DSK-002', 'LAI-DSK-003', 'LAI-DSK-004', 'LAI-DSK-005', 'LAI-DSK-006', 'LAI-DSK-007', 'LAI-DSK-015'] as const;
export type DskCode = (typeof ERROR_CODES)[number];

export function isDskCode(value: unknown): value is DskCode {
  return typeof value === 'string' && (ERROR_CODES as readonly string[]).includes(value);
}

/** The buttons of an error page. `open-portal` is on every page. */
export const ERROR_ACTIONS = ['copy-command', 'logout', 'start-agent', 'open-portal', 'open-browser'] as const;
export type ErrorAction = (typeof ERROR_ACTIONS)[number];

/** The one IPC channel the error page uses (preload/shell.ts invokes it, startup.ts handles it). */
export const ERROR_ACTION_CHANNEL = 'localai:error-action' as const;

export interface ErrorCopy {
  severity: Severity;
  title: Bilingual;
  /** What happened, in a sentence a non-expert understands. */
  message: Bilingual;
  /** What to do. */
  fix: Bilingual;
  /** A command the user can copy (`copy-command` copies exactly this). */
  command?: string;
  actions: readonly ErrorAction[];
}

export const ERROR_COPY: Readonly<Record<DskCode, ErrorCopy>> = {
  'LAI-DSK-001': {
    severity: 'error',
    title: { en: "This user isn't allowed to control Local AI", es: 'Este usuario no puede controlar Local AI' },
    message: { en: 'This user is not allowed to control Local AI yet.', es: 'Este usuario todavía no tiene permiso para controlar Local AI.' },
    fix: {
      en: 'Run this command in a terminal, then log out and log back in:',
      es: 'Ejecuta este comando en una terminal; luego cierra sesión y vuelve a entrar:',
    },
    command: 'sudo usermod -aG localai-operators $USER',
    actions: ['copy-command', 'open-portal'],
  },
  'LAI-DSK-002': {
    severity: 'warning',
    title: { en: "You were added to the operators group but haven't logged in again", es: 'Te agregaron al grupo de operadores pero no has vuelto a entrar' },
    message: {
      en: 'You were added to the Local AI operators group, but you need to log out and log back in once.',
      es: 'Te agregaron al grupo de operadores de Local AI, pero debes cerrar sesión y volver a entrar una vez.',
    },
    fix: { en: 'Log out and log back in once, then open Local AI again.', es: 'Cierra sesión y vuelve a entrar una vez; luego abre Local AI otra vez.' },
    actions: ['logout', 'open-portal'],
  },
  'LAI-DSK-003': {
    severity: 'error',
    title: { en: "The agent's key file doesn't exist yet", es: 'El archivo de clave del agente todavía no existe' },
    message: {
      en: "The Local AI agent has not created its key file yet. It is not installed or it has not finished starting for the first time.",
      es: 'El agente de Local AI todavía no creó su archivo de clave. No está instalado o no terminó de iniciar por primera vez.',
    },
    fix: {
      en: 'Press "Start agent". If it keeps happening, reinstall with install-node.sh (runbook 05).',
      es: 'Presiona "Iniciar agente". Si sigue pasando, reinstala con install-node.sh (manual 05).',
    },
    actions: ['start-agent', 'open-portal'],
  },
  'LAI-DSK-004': {
    severity: 'error',
    title: { en: 'The Local AI agent is not running', es: 'El agente de Local AI no está funcionando' },
    message: {
      en: 'Local AI is not running on this computer, so there is nothing to show yet.',
      es: 'Local AI no está funcionando en esta computadora, así que todavía no hay nada que mostrar.',
    },
    fix: {
      en: 'Press "Start agent" and enter the administrator password.',
      es: 'Presiona "Iniciar agente" e ingresa la contraseña de administrador.',
    },
    actions: ['start-agent', 'open-portal'],
  },
  'LAI-DSK-005': {
    severity: 'error',
    title: { en: "The agent couldn't be started", es: 'No se pudo iniciar el agente' },
    message: { en: "The Local AI agent couldn't be started from this app.", es: 'No se pudo iniciar el agente de Local AI desde esta aplicación.' },
    fix: {
      en: 'Open a terminal, run this command and follow what it says:',
      es: 'Abre una terminal, ejecuta este comando y sigue lo que indique:',
    },
    command: 'sudo aictl doctor',
    actions: ['copy-command', 'open-portal'],
  },
  'LAI-DSK-006': {
    severity: 'error',
    title: { en: "ai-desktop and the agent are different versions that don't work together", es: 'ai-desktop y el agente son versiones que no funcionan juntas' },
    message: {
      en: 'This app and the Local AI agent on this computer are different versions that cannot work together.',
      es: 'Esta aplicación y el agente de Local AI de esta computadora son versiones distintas que no pueden funcionar juntas.',
    },
    fix: {
      en: 'Update both from the rebuild kit, or run this command (the Survival Portal still works):',
      es: 'Actualiza los dos desde el kit de reconstrucción, o ejecuta este comando (el Portal de Supervivencia sigue funcionando):',
    },
    command: 'sudo aictl update',
    actions: ['copy-command', 'open-portal'],
  },
  'LAI-DSK-007': {
    severity: 'error',
    title: { en: "The agent refused this app's login", es: 'El agente rechazó el inicio de sesión de esta aplicación' },
    message: {
      en: "The Local AI agent did not accept this app's key. Meanwhile you can use a browser: press \"Open in browser\" and type the login code from `aictl ui-code`.",
      es: 'El agente de Local AI no aceptó la clave de esta aplicación. Mientras tanto puedes usar un navegador: presiona "Abrir en el navegador" y escribe el código de acceso de `aictl ui-code`.',
    },
    fix: {
      en: 'Run this command, then restart ai-desktop:',
      es: 'Ejecuta este comando y reinicia ai-desktop:',
    },
    command: 'sudo aictl token rotate',
    actions: ['copy-command', 'open-browser', 'open-portal'],
  },
  'LAI-DSK-015': {
    severity: 'critical',
    title: { en: "The app's security sandbox couldn't start", es: 'No pudo iniciar el entorno de seguridad de la aplicación' },
    message: {
      en: "ai-desktop's security sandbox could not start on this computer.",
      es: 'El entorno de seguridad de ai-desktop no pudo iniciar en esta computadora.',
    },
    fix: {
      en: 'Reinstall ai-desktop from the .deb file with this command:',
      es: 'Reinstala ai-desktop desde el archivo .deb con este comando:',
    },
    command: 'sudo apt install ./ai-desktop_<version>_amd64.deb',
    actions: ['copy-command', 'open-portal'],
  },
};

/** The text of a page or check in one language. */
export interface ErrorText {
  code: DskCode;
  severity: Severity;
  title: string;
  message: string;
  fix: string;
  command?: string;
  actions: readonly ErrorAction[];
}

export function errorText(code: DskCode, lang: Lang): ErrorText {
  const copy = ERROR_COPY[code];
  return {
    code,
    severity: copy.severity,
    title: copy.title[lang],
    message: copy.message[lang],
    fix: copy.fix[lang],
    ...(copy.command !== undefined ? { command: copy.command } : {}),
    actions: copy.actions,
  };
}

/** Labels of the error page's buttons and fixed words. */
export const PAGE_STRINGS = {
  code: { en: 'Code', es: 'Código' },
  whatToDo: { en: 'What to do', es: 'Qué hacer' },
  copyCommand: { en: 'Copy command', es: 'Copiar comando' },
  copied: { en: 'Copied', es: 'Copiado' },
  logout: { en: 'Log out now', es: 'Cerrar sesión ahora' },
  logoutHint: {
    en: 'If nothing happens, log out from the system menu (top right) and log back in.',
    es: 'Si no pasa nada, cierra sesión desde el menú del sistema (arriba a la derecha) y vuelve a entrar.',
  },
  startAgent: { en: 'Start agent', es: 'Iniciar agente' },
  starting: { en: 'Starting…', es: 'Iniciando…' },
  startingHint: {
    en: 'If a password window appears, enter the administrator password. This can take up to 30 seconds.',
    es: 'Si aparece una ventana de contraseña, ingresa la contraseña de administrador. Puede tardar hasta 30 segundos.',
  },
  statusExcerpt: { en: 'Service status (read-only)', es: 'Estado del servicio (solo lectura)' },
  noStatusExcerpt: { en: 'The service status could not be read.', es: 'No se pudo leer el estado del servicio.' },
  openPortal: { en: 'Open Survival Portal', es: 'Abrir el Portal de Supervivencia' },
  openBrowser: { en: 'Open in browser', es: 'Abrir en el navegador' },
  browserHint: {
    en: 'In a terminal, run `aictl ui-code` and type the six digits in the page that opens.',
    es: 'En una terminal, ejecuta `aictl ui-code` y escribe los seis dígitos en la página que se abre.',
  },
  unitState: { en: 'Service state', es: 'Estado del servicio' },
  actionFailed: { en: 'That did not work. Read the steps above.', es: 'No funcionó. Lee los pasos de arriba.' },
  unknownTitle: { en: 'Something went wrong', es: 'Algo salió mal' },
  unknownFix: { en: 'You can still use the Survival Portal.', es: 'Todavía puedes usar el Portal de Supervivencia.' },
} as const satisfies Record<string, Bilingual>;

export const ACTION_LABEL: Readonly<Record<ErrorAction, Bilingual>> = {
  'copy-command': PAGE_STRINGS.copyCommand,
  logout: PAGE_STRINGS.logout,
  'start-agent': PAGE_STRINGS.startAgent,
  'open-portal': PAGE_STRINGS.openPortal,
  'open-browser': PAGE_STRINGS.openBrowser,
};
