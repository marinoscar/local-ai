// Music types of the E11 contract (agent `/api/v1/music/*`, docs/specs/music.md) and the transport the MusicStudio and
// the SongLibrary talk through. The agent UI implements it over its REST API and SSE stream; the cloud over
// `/api/localai/music/*`. The components never fetch by themselves.

/** How a sample is used: its sound and style ("Sounds like this") or the song itself with new lyrics. */
export type SampleMode = 'sounds_like' | 'same_song';

/** `GET /music/status`. */
export interface MusicStatus {
  installed: boolean;
  loaded_model: string | null;
  engine_state: 'stopped' | 'starting' | 'ready' | 'stopping' | 'failed';
  queue: { running: number; queued: number; max: number };
}

/** What the studio asks for: the multipart fields of `POST /music/generate`. */
export interface GenerateRequest {
  lyrics: string;
  tags: string;
  duration_s: number;
  language: string;
  seed: number | null;
  variations: number;
  /** The uploaded sample, when there is one. A File in the browser (its name and type are sent). */
  sample: File | null;
  /** Only with a sample. */
  sample_mode: SampleMode | null;
  /** Only with a sample: 0..1. */
  strength: number | null;
}

/** `202` of `POST /music/generate`. */
export interface GenerateAccepted {
  op_id: string;
  song_ids: string[];
}

export type MusicOpState = 'queued' | 'running' | 'done' | 'failed' | 'cancelled';

/** `GET /music/ops/{op_id}`, and the SSE `music.progress` / `music.done` events. */
export interface MusicOp {
  op_id: string;
  state: MusicOpState;
  /** 0..1, or null while it has no measure (queued). */
  progress: number | null;
  song_ids: string[];
  error: { code: string; message: string; fix?: string } | null;
}

/** One song of `GET /music/songs`. */
export interface Song {
  id: string;
  created_at: string;
  /** The first lyric line, or the tags. */
  title: string;
  tags: string;
  language: string;
  duration_s: number;
  model_id: string;
  seed: number | null;
  mime: string;
  size_bytes: number;
  origin: 'local' | 'cloud';
}

export interface SongsPage {
  songs: Song[];
  next_cursor: string | null;
}

/** The part of the transport the SongLibrary uses. */
export interface SongTransport {
  listSongs(opts?: { cursor?: string | null; limit?: number }): Promise<SongsPage>;
  /**
   * The URL to play and download a song. The agent answers a same-origin path (its session cookie goes along); the
   * cloud may answer a short-lived signed URL, hence the promise.
   */
  audioUrl(song: Song): string | Promise<string>;
  /**
   * Deletes a song. `pin` is sent only after the node asked for the admin PIN (403 LAI-SEC-002): the agent turns it into
   * a PIN grant; a transport without a PIN (the cloud checks its own permission) ignores it.
   */
  deleteSong(id: string, opts?: { pin?: string }): Promise<void>;
}

/**
 * MusicTransport is how the studio reaches a node.
 *
 * - `generate` resolves with the operation, or rejects with a TransportError (doctor code, message, fix).
 * - `watch` follows one operation and calls `onUpdate` with each state until it ends (`done`, `failed`, `cancelled`);
 *   it returns the function that stops following. The agent uses SSE with polling as the safety net; the cloud polls.
 * - `cancel` asks the node to stop the operation; the end arrives through `watch` as `cancelled`.
 */
export interface MusicTransport extends SongTransport {
  status(): Promise<MusicStatus>;
  generate(req: GenerateRequest): Promise<GenerateAccepted>;
  watch(opId: string, onUpdate: (op: MusicOp) => void): () => void;
  cancel(opId: string): Promise<void>;
}
