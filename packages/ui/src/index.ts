// @localai/ui: the components the cloud app and the desktop shell share (docs/decisions/0004-desktop-thin-shell.md).
// Props are plain data and nothing in here fetches, so each host decides where the data comes from.
// The generated agent API types are a separate import: `@localai/ui/src/api/agent.gen`.
export * from './lab';
