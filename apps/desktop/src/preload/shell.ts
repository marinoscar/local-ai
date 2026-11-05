// Preload bridge for shell pages: window.localaiDesktop (docs/specs/desktop.md section 3.5).
// It is the whole IPC surface of the top bar, the Lab, the error page and the later windows. Every call is an
// `ipcRenderer.invoke/send/sendSync` on a fixed channel with plain data; the main process checks the calling frame's
// origin on every one (src/main/ipc.ts). Nothing from Node is exposed.
//
// A sandboxed preload cannot load a shared chunk, so the channel names are repeated here; tests/unit/preload.test.ts
// proves they match CHANNELS in src/main/ipc.ts. Only types are imported from the shared module.
import { contextBridge, ipcRenderer } from 'electron';
import type { DesktopStatus, Lang, LocalaiDesktop, TopBarNotice } from '../shared/types';

export const BRIDGE = 'localaiDesktop' as const;

const C = {
  status: 'localai:status',
  statusChanged: 'localai:status-changed',
  notice: 'localai:notice',
  startAgent: 'localai:start-agent',
  copyApiKey: 'localai:copy-api-key',
  connectionInfo: 'localai:connection-info',
  revealKey: 'localai:reveal-key',
  setMode: 'localai:set-mode',
  openWindow: 'localai:open-window',
  showTab: 'localai:show-tab',
  setFullScreen: 'localai:set-full-screen',
  closePortal: 'localai:close-portal',
  showMenu: 'localai:show-menu',
  cloudBeginLink: 'localai:cloud-begin-link',
  cloudOpenVerification: 'localai:cloud-open-verification',
  cloudLinkState: 'localai:cloud-link-state',
  cloudUnlink: 'localai:cloud-unlink',
  cloudRequest: 'localai:cloud-request',
  settingsGet: 'localai:settings-get',
  settingsSet: 'localai:settings-set',
  pinSubmit: 'localai:pin-submit',
  rotateKey: 'localai:rotate-key',
  lang: 'localai:lang',
  errorAction: 'localai:error-action',
} as const;

/** The three `ipcRenderer` methods the bridge uses, so a test can drive it without Electron. */
export interface IpcRendererLike {
  invoke(channel: string, ...args: unknown[]): Promise<unknown>;
  send(channel: string, ...args: unknown[]): void;
  sendSync(channel: string, ...args: unknown[]): unknown;
  on(channel: string, listener: (event: unknown, ...args: unknown[]) => void): unknown;
  removeListener(channel: string, listener: (event: unknown, ...args: unknown[]) => void): unknown;
}

export function createShellBridge(ipc: IpcRendererLike): LocalaiDesktop {
  const invoke = <T>(channel: string, ...args: unknown[]): Promise<T> => ipc.invoke(channel, ...args) as Promise<T>;
  const subscribe = <T>(channel: string, cb: (value: T) => void): (() => void) => {
    const listener = (_event: unknown, value: unknown): void => cb(value as T);
    ipc.on(channel, listener);
    return () => {
      ipc.removeListener(channel, listener);
    };
  };
  return {
    status: () => invoke<DesktopStatus>(C.status),
    onStatus: (cb) => subscribe<DesktopStatus>(C.statusChanged, cb),
    startAgent: () => invoke(C.startAgent),
    copyApiKey: () => invoke(C.copyApiKey),
    connectionInfo: () => invoke(C.connectionInfo),
    revealKey: () => invoke(C.revealKey),
    setMode: (target) => invoke(C.setMode, target),
    openWindow: (which) => {
      ipc.send(C.openWindow, which);
    },
    showTab: (tab) => {
      ipc.send(C.showTab, tab);
    },
    setFullScreen: (flag) => {
      ipc.send(C.setFullScreen, flag);
    },
    closePortal: () => {
      ipc.send(C.closePortal);
    },
    cloud: {
      beginLink: () => invoke(C.cloudBeginLink),
      openVerification: () => {
        ipc.send(C.cloudOpenVerification);
      },
      linkState: () => invoke(C.cloudLinkState),
      unlink: () => invoke(C.cloudUnlink),
      request: (req) => invoke(C.cloudRequest, req),
    },
    settings: {
      get: () => invoke(C.settingsGet),
      set: (patch) => invoke(C.settingsSet, patch),
    },
    pinSubmit: (pin) => invoke(C.pinSubmit, pin),
    rotateKey: () => invoke(C.rotateKey),
    lang: (): Lang => (ipc.sendSync(C.lang) === 'es' ? 'es' : 'en'),
    errorAction: (action, code) => invoke(C.errorAction, { action, code }),
    showMenu: () => {
      ipc.send(C.showMenu);
    },
    onNotice: (cb) => subscribe<TopBarNotice>(C.notice, cb),
  };
}

if (contextBridge && ipcRenderer) {
  contextBridge.exposeInMainWorld(BRIDGE, createShellBridge(ipcRenderer as unknown as IpcRendererLike));
}
