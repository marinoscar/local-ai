// The music routes of the cloud (docs/specs/cloud-app.md section 3.9, #513) and the workload switch. The page never
// talks to a node: it asks the cloud, which queues a command the node pulls. Shapes mirror
// `apps/api/src/localai/music/music.views.ts`.
import { api } from '../../api';
import type { CommandView } from '../api/types';

export type MusicReadiness = 'ready' | 'can_load' | 'too_little_vram' | 'offline' | 'no_music';
export type MusicStage = 'queued' | 'generating' | 'done' | 'failed' | 'expired' | 'cancelled';
export type SampleMode = 'sounds_like' | 'same_song';

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
  lyricsBytes: number;
  durationS: number;
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

/** What the form sends (the multipart field names of `POST /api/localai/music/requests`). */
export interface MusicRequestInput {
  nodeId: string;
  lyrics: string;
  tags: string;
  durationS: number;
  language: string;
  variations: number;
  seed?: number;
  sample?: File;
  sampleMode?: SampleMode;
  strength?: number;
}

/** The limits the API enforces (LAI-SVC-154), checked here first so a person sees them before uploading. */
export const LYRICS_MAX_BYTES = 8 * 1024;
export const TAGS_MAX_BYTES = 512;
export const SAMPLE_MAX_BYTES = 20 * 1024 * 1024;
export const utf8Bytes = (text: string): number => new TextEncoder().encode(text).length;

const music = '/localai/music';

export const listMusicNodes = (): Promise<MusicNode[]> => api.get<MusicNode[]>(`${music}/nodes`);

export function createMusicRequest(input: MusicRequestInput): Promise<MusicRequest> {
  const form = new FormData();
  form.append('nodeId', input.nodeId);
  form.append('lyrics', input.lyrics);
  form.append('tags', input.tags);
  form.append('duration_s', String(input.durationS));
  form.append('language', input.language);
  form.append('variations', String(input.variations));
  if (input.seed !== undefined) form.append('seed', String(input.seed));
  if (input.sample) {
    if (input.sampleMode) form.append('sample_mode', input.sampleMode);
    if (input.strength !== undefined) form.append('strength', String(input.strength));
    form.append('sample', input.sample, input.sample.name);
  }
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

/** Queues `localai.workload.activate`; the answer is the command, polled at `GET /api/localai/commands/:id`. */
export const activateWorkload = (nodeId: string, name: string): Promise<CommandView> =>
  api.post<CommandView>(`/localai/nodes/${encodeURIComponent(nodeId)}/workloads/${encodeURIComponent(name)}/activate`, {});
