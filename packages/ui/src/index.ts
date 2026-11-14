// @localai/ui: the components the cloud app and the desktop shell share (docs/decisions/0004-desktop-thin-shell.md).
// Props are plain data and nothing in here fetches, so each host decides where the data comes from.
// The generated agent API types are a separate import: `@localai/ui/src/api/agent.gen`.
export * from './lab';
export { LangProvider, pick, useLang, type Lang } from './lang';
// E11 (#512): the music studio, the song list and the workload switcher. MUI-free, so the agent UI imports them from
// the `@localai/ui/music` and `@localai/ui/workloads` subpaths without pulling the MUI-based lab components above.
export { MusicStudio, SongLibrary, type MusicStudioProps, type SongLibraryProps } from './music';
export type { GenerateAccepted, GenerateRequest, MusicOp, MusicOpState, MusicStatus, MusicTransport, SampleMode, Song, SongsPage, SongTransport } from './music';
export { WorkloadSwitcher, type WorkloadSwitcherProps } from './workloads';
export type { ActivateOptions, Workload, WorkloadProgress, WorkloadsResponse, WorkloadTransport } from './workloads';
export { ErrorNotice, type ErrorNoticeProps } from './common/ErrorNotice';
export { TransportError, codeFix, toErrorInfo, type TransportErrorInfo } from './common/errors';
