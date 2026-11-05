// Preload of the PIN window only: window.localaiPin (docs/specs/desktop.md 3.5, 4.6).
// The PIN window is a modal owned by the main process and shows nothing but a PIN field, so it gets the two calls it
// needs and not the whole shell bridge: `submit(pin)` and `cancel()`. The main process answers them only for this
// window's webContents (src/main/ipc.ts, `pinWindowId`). The channel names are repeated here (a sandboxed preload cannot
// load a shared chunk); tests/unit/preload.test.ts proves they match CHANNELS.
import { contextBridge, ipcRenderer } from 'electron';
import type { LocalaiPin, PinResult } from '../shared/types';

export const BRIDGE = 'localaiPin' as const;

const C = {
  pinSubmit: 'localai:pin-submit',
  pinCancel: 'localai:pin-cancel',
} as const;

export interface PinIpcLike {
  invoke(channel: string, ...args: unknown[]): Promise<unknown>;
  send(channel: string, ...args: unknown[]): void;
}

export function createPinBridge(ipc: PinIpcLike): LocalaiPin {
  return {
    submit: (pin) => ipc.invoke(C.pinSubmit, pin) as Promise<PinResult>,
    cancel: () => {
      ipc.send(C.pinCancel);
    },
  };
}

if (contextBridge && ipcRenderer) {
  contextBridge.exposeInMainWorld(BRIDGE, createPinBridge(ipcRenderer as unknown as PinIpcLike));
}
