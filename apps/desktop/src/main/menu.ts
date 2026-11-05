// The top bar's menu (the three dots): a native menu popped up by the main process (docs/specs/desktop.md 3.4).
// The top bar is a 48 px view, so an HTML menu inside it would be clipped; a native menu also reads well with a screen
// reader. This module only builds the template and the texts; index.ts pops it up and supplies the actions.
import { type Lang, pick } from './i18n';

export const MODULE = 'menu' as const;

export interface MenuState {
  lang: Lang;
  /** `settings.cloud.linked`: the menu offers "Disconnect" instead of "Connect to the lab". */
  linked: boolean;
  autostart: boolean;
  checkForUpdates: boolean;
}

export interface MenuActions {
  connectionInfo(): void;
  connect(): void;
  disconnect(): void;
  setAutostart(on: boolean): void;
  setUpdates(on: boolean): void;
  about(): void;
  quit(): void;
}

/** A subset of Electron's `MenuItemConstructorOptions`, so the template is testable without Electron. */
export interface MenuTemplateItem {
  label?: string;
  type?: 'normal' | 'separator' | 'checkbox';
  checked?: boolean;
  click?: () => void;
}

const STRINGS = {
  connectionInfo: { en: 'Connection info', es: 'Información de conexión' },
  connect: { en: 'Connect to the lab', es: 'Conectar con el laboratorio' },
  disconnect: { en: 'Disconnect from the lab', es: 'Desconectar del laboratorio' },
  startAtLogin: { en: 'Start at login', es: 'Iniciar al abrir sesión' },
  checkForUpdates: { en: 'Check for updates', es: 'Buscar actualizaciones' },
  about: { en: 'About', es: 'Acerca de' },
  quit: { en: 'Quit', es: 'Salir' },
} as const;

/** What the top bar shows for a button whose feature has not landed yet. */
export const NOT_AVAILABLE = { en: 'Not available yet.', es: 'Todavía no está disponible.' } as const;

export const ABOUT = {
  title: { en: 'About Local AI', es: 'Acerca de Local AI' },
  message: { en: 'Local AI', es: 'Local AI' },
  detail: (version: string, lang: Lang): string =>
    lang === 'es' ? `Versión ${version}\nFunciona sin internet.` : `Version ${version}\nWorks without internet.`,
  close: { en: 'Close', es: 'Cerrar' },
} as const;

export function buildMenuTemplate(state: MenuState, actions: MenuActions): MenuTemplateItem[] {
  const { lang } = state;
  return [
    { label: pick(STRINGS.connectionInfo, lang), click: actions.connectionInfo },
    state.linked ? { label: pick(STRINGS.disconnect, lang), click: actions.disconnect } : { label: pick(STRINGS.connect, lang), click: actions.connect },
    { type: 'separator' },
    { label: pick(STRINGS.startAtLogin, lang), type: 'checkbox', checked: state.autostart, click: () => actions.setAutostart(!state.autostart) },
    { label: pick(STRINGS.checkForUpdates, lang), type: 'checkbox', checked: state.checkForUpdates, click: () => actions.setUpdates(!state.checkForUpdates) },
    { type: 'separator' },
    { label: pick(STRINGS.about, lang), click: actions.about },
    { label: pick(STRINGS.quit, lang), click: actions.quit },
  ];
}
