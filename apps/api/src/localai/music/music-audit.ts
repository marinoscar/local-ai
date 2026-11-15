// Audit actions of the music feature (E11, docs/specs/cloud-app.md section 3.5). Permanent strings: the audit page
// filters on them. The meta of each row holds ids, counts and flags only: never lyrics, tags, sample bytes or a URL.
export const AUDIT_MUSIC_REQUESTED = 'localai.music.requested';
export const AUDIT_MUSIC_REQUEST_CANCELLED = 'localai.music.request_cancelled';
export const AUDIT_MUSIC_SONG_DELETED = 'localai.music.song_deleted';
/** A saved prompt (#568) was created, changed or deleted. Meta: ids only (`promptId` is the target; never the text). */
export const AUDIT_MUSIC_PROMPT_CREATED = 'localai.music.prompt_created';
export const AUDIT_MUSIC_PROMPT_UPDATED = 'localai.music.prompt_updated';
export const AUDIT_MUSIC_PROMPT_DELETED = 'localai.music.prompt_deleted';

/** `targetType` of a request row (`targetId` is `localai_music_requests.id`). */
export const AUDIT_TARGET_MUSIC_REQUEST = 'localai_music_request';
/** `targetType` of a song row (`targetId` is `localai_music_songs.id`). */
export const AUDIT_TARGET_MUSIC_SONG = 'localai_music_song';
/** `targetType` of a prompt row (`targetId` is `localai_music_prompts.id`). */
export const AUDIT_TARGET_MUSIC_PROMPT = 'localai_music_prompt';

export const MUSIC_AUDIT_ACTIONS = [
  AUDIT_MUSIC_REQUESTED,
  AUDIT_MUSIC_REQUEST_CANCELLED,
  AUDIT_MUSIC_SONG_DELETED,
  AUDIT_MUSIC_PROMPT_CREATED,
  AUDIT_MUSIC_PROMPT_UPDATED,
  AUDIT_MUSIC_PROMPT_DELETED,
] as const;

/**
 * A workload switch was asked for (cloud-app.md section 3.4), beside the command plane's own `localai.command.requested`.
 * `targetType` is the node (`localai_node`), `targetId` its lab id; meta `{ name, commandId }`.
 */
export const AUDIT_WORKLOAD_REQUESTED = 'localai.workload.requested';
