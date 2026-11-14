// Music (E11, #512): the song form and the song list, shared by the agent UI (/music/), the cloud (/music) and the
// desktop shell. Plain React, no MUI and no inline style (the agent's Portal CSP): style them through the `lai-*`
// classes; `@localai/ui/creative.css` is a neutral base. Words come from the nearest LangProvider (English and Spanish).
export { MusicStudio, CODE_SONG_CANCELLED, CODE_SONG_FAILED, type MusicStudioProps } from './MusicStudio';
export { SongLibrary, CODE_LIBRARY_FAILED, type SongLibraryProps } from './SongLibrary';
export { MUSIC_STRINGS, SONG_LANGUAGES, type MusicKey } from './strings';
export { fileNameOf, formatBytes, formatDate, formatDuration } from './format';
export * from './validate';
export type {
  GenerateAccepted,
  GenerateRequest,
  MusicOp,
  MusicOpState,
  MusicStatus,
  MusicTransport,
  SampleMode,
  Song,
  SongsPage,
  SongTransport,
} from './types';
export { ErrorNotice, type ErrorNoticeProps } from '../common/ErrorNotice';
export { TransportError, codeFix, codeMeaning, isDoctorCode, toErrorInfo, type TransportErrorInfo } from '../common/errors';
export { LangProvider, useLang, type Lang } from '../lang';
