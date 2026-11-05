// Preload bridge for shell pages: window.localaiDesktop (docs/specs/desktop.md section 3.5).
// DSK-03 exposes only what the error page needs (`errorAction`). DSK-04 adds the rest of the allowlisted surface,
// with origin checks on every handler, and folds this call into it.
import { contextBridge, ipcRenderer } from 'electron';

export const BRIDGE = 'localaiDesktop' as const;
/** Same string as ERROR_ACTION_CHANNEL in src/main/i18n.ts (not imported, to keep the sandboxed preload tiny). */
const ERROR_ACTION_CHANNEL = 'localai:error-action';

if (contextBridge && ipcRenderer) {
  contextBridge.exposeInMainWorld(BRIDGE, {
    /** A button of the error page. The main process checks the sender's page and that the code owns the button. */
    errorAction: (action: string, code: string): Promise<{ ok: boolean; code?: string; statusExcerpt?: string; busy?: boolean }> =>
      ipcRenderer.invoke(ERROR_ACTION_CHANNEL, { action, code }) as Promise<{ ok: boolean; code?: string; statusExcerpt?: string; busy?: boolean }>,
  });
}
