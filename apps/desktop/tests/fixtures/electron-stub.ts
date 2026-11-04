// What the unit tests see when a main-process module imports `electron` (vitest.config.mts aliases it here).
// It is deliberately empty: a test that needs a piece of the Electron API passes its own fake in.
export const app = undefined;
export const BrowserWindow = undefined;
export const contextBridge = undefined;
export const ipcRenderer = undefined;
export default {};
