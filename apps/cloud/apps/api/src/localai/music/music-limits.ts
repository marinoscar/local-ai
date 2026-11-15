// The limits of the music feature (E11 contract sections 3 and 6). The input limits are the agent's own
// (`POST /music/generate`), re-checked here so a request the node would refuse is refused at once, with the same code.
import { COMMAND_POLICY } from '../commands/command-policy';
import {
  MUSIC_DURATION_MAX_S,
  MUSIC_DURATION_MIN_S,
  MUSIC_LYRICS_MAX_BYTES,
  MUSIC_SAMPLE_MAX_BYTES,
  MUSIC_TAGS_MAX_BYTES,
  MUSIC_VARIATIONS_MAX,
} from '../contracts/commands';

export { MUSIC_DURATION_MAX_S, MUSIC_DURATION_MIN_S, MUSIC_LYRICS_MAX_BYTES, MUSIC_SAMPLE_MAX_BYTES, MUSIC_TAGS_MAX_BYTES, MUSIC_VARIATIONS_MAX };

/** The object-storage key prefix of the feature (registered in `platform/storage/storage-prefixes.ts`). */
export const MUSIC_KEY_PREFIX_ID = 'localai-music';
export const MUSIC_KEY_PREFIX = 'localai-music/';

/**
 * How long the sample's signed GET and the songs' signed PUT URLs live: they are minted when a node CLAIMS the command
 * (`music-claim.ts`), so the longest the command may run from its claim (the hard cap, one hour) is enough. Nothing
 * signed is minted earlier, and no signed URL is stored.
 */
export const MUSIC_NODE_URL_TTL_S = Math.ceil(COMMAND_POLICY['localai.music.generate'].hardCapMs / 1000);

/**
 * What the stored params hold until the claim: an https URL on the reserved `.invalid` domain (RFC 2606), valid for the
 * strict schema, unreachable for anyone. A node that somehow gets it fails the download with LAI-SVC-156.
 */
export const MUSIC_PENDING_URL = 'https://signed-at-claim.invalid/localai-music';

/** How long the browser's signed GET of a song lives (play or download): 5 minutes. */
export const MUSIC_PLAY_URL_TTL_S = 300;

/** Per person: requests waiting or running at once (a node's own queue holds 4). */
export const MUSIC_MAX_ACTIVE_PER_USER = 3;
/** Per person: requests in a rolling hour. */
export const MUSIC_MAX_PER_HOUR_PER_USER = 30;

/** The multipart body: the form fields and one file. Fields are capped above the lyrics limit so an overlong field is seen, not cut. */
export const MUSIC_MULTIPART_FIELD_MAX_BYTES = 2 * MUSIC_LYRICS_MAX_BYTES;
export const MUSIC_MULTIPART_MAX_FIELDS = 24;

/** A song's title (typed, generated or suggested, #568): at most 80 characters. */
export const MUSIC_TITLE_MAX_CHARS = 80;

/**
 * "Describe your song" (#568): at most 500 characters. It is sent to the node as the command's `tags`, whose limit is
 * the agent's 512 UTF-8 bytes (`MUSIC_TAGS_MAX_BYTES`), so the description must fit BOTH: 500 characters and 512 bytes.
 * The agent's limit is deliberately not raised: nodes already in the field (and offline ones) would refuse a longer one.
 */
export const MUSIC_DESCRIPTION_MAX_CHARS = 500;

/** What "No vocals" sends as the lyrics: the shim's instrumental form (spike #505). */
export const MUSIC_INSTRUMENTAL_LYRICS = '[Instrumental]';

/** Per page of the prompt history. */
export const MUSIC_PROMPTS_PAGE_SIZE_MAX = 50;

/**
 * The length sent to a node that cannot pick one itself (#571): its capabilities do not list `music.auto_duration`, so
 * an Auto request goes out as 60 s and the page says so ("This computer needs an update to pick the length by itself").
 */
export const MUSIC_DURATION_FALLBACK_S = 60;

/** The default strength per mode (E11 contract section 3): 1.0 for the same song, 0.5 for "sounds like". */
export const MUSIC_DEFAULT_STRENGTH = { same_song: 1.0, sounds_like: 0.5 } as const;
