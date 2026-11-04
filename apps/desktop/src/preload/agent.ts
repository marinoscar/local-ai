// Minimal preload bridge for agent pages: window.localaiDesktopHost (docs/specs/desktop.md section 3.5).
// Scaffold stub (DSK-01): exposes nothing yet. DSK-04 adds the allowlisted IPC surface with origin checks.
export const BRIDGE = 'localaiDesktopHost' as const;
