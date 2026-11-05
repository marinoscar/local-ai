// Preload bridge for shell pages: window.localaiDesktop (docs/specs/desktop.md section 3.5).
// DSK-03 exposed only what the error page needs (`errorAction`); DSK-06 adds `setMode`. DSK-04 adds the rest of the allowlisted surface,
// with origin checks on every handler, and folds this call into it.
import { contextBridge, ipcRenderer } from 'electron';

export const BRIDGE = 'localaiDesktop' as const;
/** Same string as ERROR_ACTION_CHANNEL in src/main/i18n.ts (not imported, to keep the sandboxed preload tiny). */
const ERROR_ACTION_CHANNEL = 'localai:error-action';
/** Same string as SET_MODE_CHANNEL in src/main/tray.ts. */
const SET_MODE_CHANNEL = 'localai:set-mode';

if (contextBridge && ipcRenderer) {
  contextBridge.exposeInMainWorld(BRIDGE, {
    /** A button of the error page. The main process checks the sender's page and that the code owns the button. */
    errorAction: (action: string, code: string): Promise<{ ok: boolean; code?: string }> => ipcRenderer.invoke(ERROR_ACTION_CHANNEL, { action, code }) as Promise<{ ok: boolean; code?: string }>,
    /** Asks the main process to switch the node's mode (the same path as the tray). Survival needs no PIN; leaving it does. */
    setMode: (target: 'online' | 'offline' | 'survival'): Promise<{ ok: boolean; code?: string }> => ipcRenderer.invoke(SET_MODE_CHANNEL, target) as Promise<{ ok: boolean; code?: string }>,
  });
}
