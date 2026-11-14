// Audit actions of the music feature (E11, docs/specs/cloud-app.md section 3.5). Permanent strings: the audit page
// filters on them. The meta of each row holds ids, counts and flags only: never lyrics, tags, sample bytes or a URL.
export const AUDIT_MUSIC_REQUESTED = 'localai.music.requested';
export const AUDIT_MUSIC_REQUEST_CANCELLED = 'localai.music.request_cancelled';
export const AUDIT_MUSIC_SONG_DELETED = 'localai.music.song_deleted';

/** `targetType` of a request row (`targetId` is `localai_music_requests.id`). */
export const AUDIT_TARGET_MUSIC_REQUEST = 'localai_music_request';
/** `targetType` of a song row (`targetId` is `localai_music_songs.id`). */
export const AUDIT_TARGET_MUSIC_SONG = 'localai_music_song';

export const MUSIC_AUDIT_ACTIONS = [AUDIT_MUSIC_REQUESTED, AUDIT_MUSIC_REQUEST_CANCELLED, AUDIT_MUSIC_SONG_DELETED] as const;

/**
 * A workload switch was asked for (cloud-app.md section 3.4), beside the command plane's own `localai.command.requested`.
 * `targetType` is the node (`localai_node`), `targetId` its lab id; meta `{ name, commandId }`.
 */
export const AUDIT_WORKLOAD_REQUESTED = 'localai.workload.requested';
