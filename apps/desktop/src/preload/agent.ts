// Minimal preload bridge for agent pages: window.localaiDesktopHost (docs/specs/desktop.md section 3.5).
// Exactly five members: `isDesktop`, `version`, `copyApiKey`, `openWindow`, `connectionInfo`. Nothing else, ever: the
// agent UI is served by another program, so it gets the smallest surface that lets it adapt. The main process answers
// only frames of http://127.0.0.1:7070 (src/main/ipc.ts). The channel names are repeated here (a sandboxed preload
// cannot load a shared chunk); tests/unit/preload.test.ts proves they match CHANNELS.
import { contextBridge, ipcRenderer } from 'electron';
import type { LocalaiDesktopHost } from '../shared/types';

export const BRIDGE = 'localaiDesktopHost' as const;

const C = {
  copyApiKey: 'localai:host:copy-api-key',
  openWindow: 'localai:host:open-window',
  connectionInfo: 'localai:host:connection-info',
  version: 'localai:host:version',
} as const;

export interface HostIpcLike {
  invoke(channel: string, ...args: unknown[]): Promise<unknown>;
  send(channel: string, ...args: unknown[]): void;
  sendSync(channel: string, ...args: unknown[]): unknown;
}

export function createHostBridge(ipc: HostIpcLike): LocalaiDesktopHost {
  const answer = ipc.sendSync(C.version);
  return {
    isDesktop: true,
    version: typeof answer === 'string' ? answer : '',
    copyApiKey: () => ipc.invoke(C.copyApiKey) as ReturnType<LocalaiDesktopHost['copyApiKey']>,
    openWindow: (which) => {
      ipc.send(C.openWindow, which);
    },
    connectionInfo: () => ipc.invoke(C.connectionInfo) as ReturnType<LocalaiDesktopHost['connectionInfo']>,
  };
}

if (contextBridge && ipcRenderer) {
  contextBridge.exposeInMainWorld(BRIDGE, createHostBridge(ipcRenderer as unknown as HostIpcLike));
}
