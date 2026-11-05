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

// ---------------------------------------------------------------------------------------------------------------------
// Tray, native dialogs and notifications (DSK-06; docs/specs/desktop.md 4.3 and 4.4)
// ---------------------------------------------------------------------------------------------------------------------

/** The six status lights of the tray (`build/linux/tray/tray-<colour>.png`). */
export const TRAY_COLOURS = ['green', 'blue', 'orange', 'amber', 'red', 'grey'] as const;
export type TrayColour = (typeof TRAY_COLOURS)[number];

/** The tooltip of each colour, word for word from desktop.md 4.3. */
export const TRAY_TOOLTIPS: Readonly<Record<TrayColour, Bilingual>> = {
  green: { en: 'Local AI: ready (online)', es: 'Local AI: listo (en línea)' },
  blue: { en: 'Local AI: ready (offline)', es: 'Local AI: listo (sin conexión)' },
  orange: { en: 'Local AI: survival mode', es: 'Local AI: modo supervivencia' },
  amber: { en: 'Local AI: needs attention', es: 'Local AI: necesita atención' },
  red: { en: 'Local AI: not working, click for help', es: 'Local AI: no funciona, haz clic para ayuda' },
  grey: { en: 'Local AI: starting', es: 'Local AI: iniciando' },
};

/** The three node modes in running text ("Local AI is now in survival mode"). */
export const MODE_WORDS: Readonly<Record<'online' | 'offline' | 'survival', Bilingual>> = {
  online: { en: 'online', es: 'en línea' },
  offline: { en: 'offline', es: 'sin conexión' },
  survival: { en: 'survival', es: 'supervivencia' },
};

/** Labels of the tray menu (desktop.md 4.3). */
export const TRAY_TEXT = {
  open: { en: 'Open Local AI', es: 'Abrir Local AI' },
  chat: { en: 'Chat', es: 'Chat' },
  library: { en: 'Library', es: 'Biblioteca' },
  maps: { en: 'Maps', es: 'Mapas' },
  mode: { en: 'Mode', es: 'Modo' },
  online: { en: 'Online', es: 'En línea' },
  offline: { en: 'Offline', es: 'Sin conexión' },
  survival: { en: '⚠ Survival mode', es: '⚠ Modo supervivencia' },
  leaveSurvival: {
    en: 'Leave survival in the Survival Portal',
    es: 'Para salir de supervivencia usa el Portal de Supervivencia',
  },
  copyKey: { en: 'Copy API key', es: 'Copiar la clave de la API' },
  connectionInfo: { en: 'Show connection info', es: 'Mostrar datos de conexión' },
  lab: { en: 'Lab (ai.marin.cr)', es: 'Laboratorio (ai.marin.cr)' },
  startAgent: PAGE_STRINGS.startAgent,
  startAtLogin: { en: 'Start at login', es: 'Iniciar al entrar a la sesión' },
  quit: { en: 'Quit', es: 'Salir' },
} as const satisfies Record<string, Bilingual>;

/** Native dialogs of the tray (desktop.md 4.3, implementation note 5). */
export const DIALOG_TEXT = {
  survivalTitle: { en: 'Survival mode', es: 'Modo supervivencia' },
  survivalQuestion: {
    en: 'Switch to Survival mode? Everything you need offline will start.',
    es: '¿Cambiar a modo supervivencia? Se iniciará todo lo necesario sin internet.',
  },
  survivalYes: { en: 'Switch to Survival mode', es: 'Cambiar a modo supervivencia' },
  cancel: { en: 'Cancel', es: 'Cancelar' },
  modeFailedTitle: { en: "The mode couldn't be changed", es: 'No se pudo cambiar el modo' },
  modeFailedFallback: { en: 'Something went wrong while changing the mode.', es: 'Algo salió mal al cambiar el modo.' },
  fixLabel: { en: 'What to do', es: 'Qué hacer' },
  ok: { en: 'OK', es: 'Aceptar' },
} as const satisfies Record<string, Bilingual>;

/**
 * Codes that are a one-time notice and never a page: they have no buttons and are not in `ERROR_CODES`
 * (doctor-codes.md registers them).
 */
export const NOTICE_COPY = {
  'LAI-DSK-013': {
    severity: 'warning',
    title: { en: "The system tray isn't available", es: 'La bandeja del sistema no está disponible' },
    message: {
      en: 'Local AI cannot show its status icon here. Closing the window will quit the app.',
      es: 'Local AI no puede mostrar su icono de estado aquí. Al cerrar la ventana se cerrará la aplicación.',
    },
    fix: {
      en: 'Turn on "AppIndicator and KStatusNotifierItem Support" in Extensions, then restart ai-desktop.',
      es: 'Activa "AppIndicator and KStatusNotifierItem Support" en Extensiones y reinicia ai-desktop.',
    },
  },
  // `{path}` is the file that could not be written (the toast names it; desktop.md 4.5).
  'LAI-DSK-014': {
    severity: 'warning',
    title: { en: 'Couldn\'t turn on "Start at login"', es: 'No se pudo activar "Iniciar al entrar a la sesión"' },
    message: {
      en: 'Local AI could not write {path}, so it will not start by itself after a restart.',
      es: 'Local AI no pudo escribir {path}, así que no se iniciará solo después de reiniciar.',
    },
    fix: {
      en: 'Check that `~/.config/autostart` is writable, then try again.',
      es: 'Comprueba que `~/.config/autostart` tenga permiso de escritura e inténtalo de nuevo.',
    },
  },
} as const satisfies Record<string, { severity: Severity; title: Bilingual; message: Bilingual; fix: Bilingual }>;
export type NoticeCode = keyof typeof NOTICE_COPY;

/** `{name}` placeholders of the notification texts. */
export function fill(text: string, values: Readonly<Record<string, string>>): string {
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => values[name] ?? whole);
}

/** Texts of the native notifications (desktop.md 4.4); builders in notify.ts. Placeholders are `{model}`, `{mode}`, ... */
export const NOTIFY_TEXT = {
  modeChangedTitle: { en: 'Mode changed', es: 'Modo cambiado' },
  modeChangedBody: { en: 'Local AI is now in {mode} mode.', es: 'Local AI ahora está en modo {mode}.' },
  modelFailedTitle: { en: "A model couldn't be loaded", es: 'No se pudo cargar un modelo' },
  modelFailedBody: { en: 'The model {model} did not load ({code}). Open Local AI to see what to do.', es: 'El modelo {model} no se cargó ({code}). Abre Local AI para ver qué hacer.' },
  modelFailedBodyNoCode: { en: 'The model {model} did not load. Open Local AI to see what to do.', es: 'El modelo {model} no se cargó. Abre Local AI para ver qué hacer.' },
  syncDoneTitle: { en: 'Sync finished', es: 'Sincronización terminada' },
  syncDoneBody: { en: 'Everything is up to date.', es: 'Todo está al día.' },
  syncPartialBody: { en: 'Sync finished, but {failed} items could not be updated. The previous copies were kept.', es: 'La sincronización terminó, pero {failed} elementos no se pudieron actualizar. Se conservaron las copias anteriores.' },
  syncFailedTitle: { en: 'Sync failed', es: 'La sincronización falló' },
  syncFailedBody: { en: 'The last good copies were kept. Open Local AI to see what to do.', es: 'Se conservaron las últimas copias buenas. Abre Local AI para ver qué hacer.' },
  driveMountedTitle: { en: 'Drive connected', es: 'Unidad conectada' },
  driveMountedBody: { en: 'The Local AI drive was plugged in.', es: 'Se conectó la unidad de Local AI.' },
  driveMountedReadOnlyBody: { en: 'The Local AI drive was plugged in (read only).', es: 'Se conectó la unidad de Local AI (solo lectura).' },
  driveUnmountedTitle: { en: 'Drive removed', es: 'Unidad retirada' },
  driveUnmountedSafeBody: { en: 'The Local AI drive can be unplugged safely.', es: 'La unidad de Local AI se puede desconectar sin riesgo.' },
  driveUnmountedUnsafeBody: { en: 'The Local AI drive was removed without ejecting it. Plug it in again and eject it properly.', es: 'La unidad de Local AI se retiró sin expulsarla. Conéctala otra vez y expúlsala correctamente.' },
  alertTitle: { en: 'Local AI needs attention', es: 'Local AI necesita atención' },
  suggestTitle: { en: 'No internet', es: 'Sin internet' },
  suggestBody: {
    en: 'No internet for 30 minutes. Everything here still works. Switch to Survival mode?',
    es: 'Sin internet durante 30 minutos. Todo lo de aquí sigue funcionando. ¿Cambiar a modo supervivencia?',
  },
} as const satisfies Record<string, Bilingual>;
