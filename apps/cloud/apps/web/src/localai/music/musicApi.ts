// The music routes of the cloud (docs/specs/cloud-app.md section 3.9, #513, #568) and the workload switch. The page never
// talks to a node: it asks the cloud, which queues a command the node pulls. Shapes mirror
// `apps/api/src/localai/music/music.views.ts`.
import { api } from '../../api';
import type { CommandView } from '../api/types';

export type MusicReadiness = 'ready' | 'can_load' | 'too_little_vram' | 'offline' | 'no_music';
export type MusicStage = 'queued' | 'generating' | 'done' | 'failed' | 'expired' | 'cancelled';
export type SampleMode = 'sounds_like' | 'same_song';
/**
 * How the song gets its words (#568, #570): Automatic (the default: the box's lyrics, else the cloud's AI writes them
 * at Make the song when music AI is available, else none), the person's own, written by the AI now, or none.
 */
export type LyricsMode = 'auto' | 'mine' | 'ai' | 'instrumental';

export interface WorkloadItem {
  name: string;
  label: { en: string; es: string };
  fits: boolean;
  missing: string[];
}

export interface MusicNode {
  nodeId: string;
  nodeName: string;
  readiness: MusicReadiness;
  musicModels: string[];
  activeWorkload: string | null;
  /** From the node's last snapshot; null for an agent that does not report workloads. */
  workloads: WorkloadItem[] | null;
  /** The node picks a song's length itself (#571); false: an Auto song is sent to it as 1 minute. */
  autoDuration: boolean;
  lastStateAt: string | null;
}

export interface MusicSong {
  id: string;
  index: number;
  status: 'pending' | 'ready' | 'failed';
  mime: string | null;
  sizeBytes: number | null;
  durationS: number | null;
  readyAt: string | null;
}

export interface MusicRequest {
  id: string;
  nodeId: string;
  nodeName: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'expired' | 'cancelled';
  stage: MusicStage;
  title: string;
  tags: string;
  /** The saved prompt it was made from; null once that prompt is deleted. */
  promptId: string | null;
  lyricsMode: LyricsMode | null;
  lyricsBytes: number;
  /** The length the node was asked for; null: Auto (#571), the songs carry the length it chose. */
  durationS: number | null;
  language: string;
  seed: number | null;
  variations: number;
  hasSample: boolean;
  sampleMode: SampleMode | null;
  strength: number | null;
  commandId: string | null;
  error: { code: string | null; message: string | null } | null;
  requestedByMe: boolean;
  lyrics?: string;
  createdAt: string;
  finishedAt: string | null;
  songs: MusicSong[];
}

export interface LibrarySong extends MusicSong {
  requestId: string;
  /** Null when the prompt it was made from was deleted (the song stays). */
  promptId: string | null;
  nodeId: string;
  nodeName: string;
  title: string;
  tags: string;
  language: string;
  seed: number | null;
  createdAt: string;
  requestedByMe: boolean;
}

export interface Page<T> {
  items: T[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

/** One saved prompt in the history (#568): no lyrics (they come with the single prompt). */
export interface MusicPromptSummary {
  id: string;
  title: string;
  description: string;
  lyricsMode: LyricsMode;
  hasSample: boolean;
  songCount: number;
  lastUsedAt: string;
  createdAt: string;
  updatedAt: string;
}

/** One saved prompt, everything the form loads. */
export interface MusicPrompt extends MusicPromptSummary {
  lyrics: string;
  /** null: Auto (#571). */
  durationS: number | null;
  language: string;
  seed: number | null;
  variations: number;
  sampleMime: string | null;
  sampleBytes: number | null;
  sampleMode: SampleMode | null;
  strength: number | null;
}

/** What the form saves as a prompt (`POST /prompts`, `PATCH /prompts/:id`). */
export interface MusicPromptInput {
  title: string;
  description: string;
  lyricsMode: LyricsMode;
  lyrics: string;
  /** null: Auto (#571). */
  durationS: number | null;
  language: string;
  seed: number | null;
  variations: number;
  sampleMode?: SampleMode | null;
  strength?: number | null;
  uiLanguage: 'en' | 'es';
}

/** Why music AI is not available: the AI is off, "Use AI for music" is off, or no model resolves for the person. */
export type MusicAiReason = 'ai_off' | 'music_ai_off' | 'no_model';

/** Whether music AI works for this person now (`GET /assist`), else the reason and the API's one-sentence fix. */
export interface MusicAssist {
  ai: { enabled: true; reason: null; fix: null } | { enabled: false; reason: MusicAiReason; fix: string };
}

/** "Use AI for music" (`GET`/`PUT /api/localai/music/ai-settings`, ai_config:read / ai_config:write). */
export interface MusicAiSettings {
  aiEnabled: boolean;
}

/** What the form sends (the multipart field names of `POST /api/localai/music/requests`). */
export interface MusicRequestInput {
  nodeId: string;
  title: string;
  description: string;
  lyricsMode: LyricsMode;
  lyrics: string;
  uiLanguage: 'en' | 'es';
  /** Save into (and make from) this saved prompt; without it the cloud saves a new one. */
  promptId?: string;
  /** Do not reuse the saved prompt's sample (and drop it from the prompt). */
  removeSample?: boolean;
  /** null: Auto (#571), the field is left out and the cloud decides what the node is sent. */
  durationS: number | null;
  language: string;
  variations: number;
  seed?: number;
  sample?: File;
  /** The saved prompt's sample is used (no new file): the sample settings are sent too. */
  keepsSavedSample?: boolean;
  sampleMode?: SampleMode;
  strength?: number;
}

/** The limits the API enforces (LAI-SVC-154), checked here first so a person sees them before uploading. */
export const LYRICS_MAX_BYTES = 8 * 1024;
export const TAGS_MAX_BYTES = 512;
/** "Describe your song": 500 characters, and the node's 512 bytes (it is sent as the command's tags). */
export const DESCRIPTION_MAX_CHARS = 500;
export const TITLE_MAX_CHARS = 80;
export const charCount = (text: string): number => [...text].length;
export const SAMPLE_MAX_BYTES = 20 * 1024 * 1024;
export const utf8Bytes = (text: string): number => new TextEncoder().encode(text).length;

const music = '/localai/music';

export const listMusicNodes = (): Promise<MusicNode[]> => api.get<MusicNode[]>(`${music}/nodes`);

export function createMusicRequest(input: MusicRequestInput): Promise<MusicRequest> {
  const form = new FormData();
  form.append('nodeId', input.nodeId);
  form.append('title', input.title);
  form.append('description', input.description);
  form.append('lyrics_mode', input.lyricsMode);
  form.append('lyrics', input.lyrics);
  form.append('ui_language', input.uiLanguage);
  if (input.promptId) form.append('promptId', input.promptId);
  if (input.removeSample) form.append('remove_sample', 'true');
  if (input.durationS !== null) form.append('duration_s', String(input.durationS));
  form.append('language', input.language);
  form.append('variations', String(input.variations));
  if (input.seed !== undefined) form.append('seed', String(input.seed));
  if (input.sample || input.keepsSavedSample) {
    if (input.sampleMode) form.append('sample_mode', input.sampleMode);
    if (input.strength !== undefined) form.append('strength', String(input.strength));
  }
  if (input.sample) form.append('sample', input.sample, input.sample.name);
  return api.postFormData<MusicRequest>(`${music}/requests`, form);
}

export const listActiveRequests = (): Promise<Page<MusicRequest>> => api.get<Page<MusicRequest>>(`${music}/requests?active=true&mine=true&pageSize=10`);
export const getMusicRequest = (id: string): Promise<MusicRequest> => api.get<MusicRequest>(`${music}/requests/${encodeURIComponent(id)}`);
export const cancelMusicRequest = (id: string): Promise<MusicRequest> => api.post<MusicRequest>(`${music}/requests/${encodeURIComponent(id)}/cancel`);
export const listSongs = (page = 1, pageSize = 20): Promise<Page<LibrarySong>> => api.get<Page<LibrarySong>>(`${music}/songs?page=${page}&pageSize=${pageSize}`);

/** A 5-minute signed URL of a song (the `<audio>` element cannot send the Authorization header, so it gets a URL). */
export const songAudioUrl = (id: string, download = false): Promise<{ url: string; expiresAt: string }> =>
  api.get<{ url: string; expiresAt: string }>(`${music}/songs/${encodeURIComponent(id)}/audio?as=json${download ? '&download=1' : ''}`);

/** Deletes the cloud copy (the node keeps its own). The typed confirmation is the song's id, sent by the dialog. */
export const deleteSong = (id: string): Promise<{ id: string; deleted: true }> =>
  api.delete<{ id: string; deleted: true }>(`${music}/songs/${encodeURIComponent(id)}`, { body: JSON.stringify({ confirm: id }) });

// --- the prompt history and the writing helpers (#568) ------------------------------------------------------------

export const listPrompts = (page = 1, pageSize = 20): Promise<Page<MusicPromptSummary>> => api.get<Page<MusicPromptSummary>>(`${music}/prompts?page=${page}&pageSize=${pageSize}`);
export const getPrompt = (id: string): Promise<MusicPrompt> => api.get<MusicPrompt>(`${music}/prompts/${encodeURIComponent(id)}`);
export const createPrompt = (input: MusicPromptInput & { copySampleFrom?: string }): Promise<MusicPrompt> => api.post<MusicPrompt>(`${music}/prompts`, input);
export const updatePrompt = (id: string, input: Partial<MusicPromptInput> & { removeSample?: true }): Promise<MusicPrompt> =>
  api.patch<MusicPrompt>(`${music}/prompts/${encodeURIComponent(id)}`, input);
/** Deletes a saved prompt; its songs stay. The typed confirmation is the prompt's id, sent by the dialog. */
export const deletePrompt = (id: string): Promise<{ id: string; deleted: true }> =>
  api.delete<{ id: string; deleted: true }>(`${music}/prompts/${encodeURIComponent(id)}`, { body: JSON.stringify({ confirm: id }) });
export const getAssist = (): Promise<MusicAssist> => api.get<MusicAssist>(`${music}/assist`);
export const getMusicAiSettings = (): Promise<MusicAiSettings> => api.get<MusicAiSettings>(`${music}/ai-settings`);
export const putMusicAiSettings = (value: MusicAiSettings): Promise<MusicAiSettings> => api.put<MusicAiSettings>(`${music}/ai-settings`, value);
export const suggestTitle = (input: { lyrics: string; description: string; lyricsMode: LyricsMode; language: string; uiLanguage: 'en' | 'es' }): Promise<{ title: string; source: 'ai' | 'generated' }> =>
  api.post<{ title: string; source: 'ai' | 'generated' }>(`${music}/title-suggestion`, input);
export const suggestLyrics = (input: { description: string; language: string; title?: string }): Promise<{ lyrics: string; source: 'ai' }> =>
  api.post<{ lyrics: string; source: 'ai' }>(`${music}/lyrics-suggestion`, input);

/** Queues `localai.workload.activate`; the answer is the command, polled at `GET /api/localai/commands/:id`. */
export const activateWorkload = (nodeId: string, name: string): Promise<CommandView> =>
  api.post<CommandView>(`/localai/nodes/${encodeURIComponent(nodeId)}/workloads/${encodeURIComponent(name)}/activate`, {});
