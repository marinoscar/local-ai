// Test fixtures of the music page: the API's shapes (`apps/api/src/localai/music/music.views.ts`), placeholder names only.
import type { CommandView } from '../api/types';
import type { LibrarySong, MusicNode, MusicRequest, Page, WorkloadItem } from './musicApi';

export const WORKLOADS: WorkloadItem[] = [
  { name: 'coding', label: { en: 'Coding', es: 'Programación' }, fits: true, missing: [] },
  { name: 'music', label: { en: 'Music', es: 'Música' }, fits: true, missing: [] },
  { name: 'video', label: { en: 'Video', es: 'Video' }, fits: false, missing: [] },
];

export function musicNode(partial: Partial<MusicNode> = {}): MusicNode {
  return {
    nodeId: 'ai-n2',
    nodeName: 'Studio laptop',
    readiness: 'ready',
    musicModels: ['acestep-v15-turbo'],
    activeWorkload: 'music',
    workloads: WORKLOADS,
    lastStateAt: '2026-11-14T12:00:00.000Z',
    ...partial,
  };
}

export function musicRequest(partial: Partial<MusicRequest> = {}): MusicRequest {
  return {
    id: '5d1c0b7e-2f43-4a51-9a0e-7b6c1d2e3f40',
    nodeId: 'ai-n2',
    nodeName: 'Studio laptop',
    status: 'queued',
    stage: 'queued',
    title: 'Walking to the river',
    tags: 'folk',
    lyricsBytes: 40,
    durationS: 60,
    language: 'en',
    seed: null,
    variations: 1,
    hasSample: false,
    sampleMode: null,
    strength: null,
    commandId: 'c0000000-0000-4000-8000-000000000001',
    error: null,
    requestedByMe: true,
    createdAt: '2026-11-14T12:00:00.000Z',
    finishedAt: null,
    songs: [{ id: 'a0000000-0000-4000-8000-000000000001', index: 0, status: 'pending', mime: null, sizeBytes: null, durationS: null, readyAt: null }],
    ...partial,
  };
}

export function librarySong(partial: Partial<LibrarySong> = {}): LibrarySong {
  return {
    id: 'a0000000-0000-4000-8000-000000000009',
    index: 0,
    status: 'ready',
    mime: 'audio/wav',
    sizeBytes: 1000,
    durationS: 61,
    readyAt: '2026-11-14T12:02:00.000Z',
    requestId: '5d1c0b7e-2f43-4a51-9a0e-7b6c1d2e3f40',
    nodeId: 'ai-n2',
    nodeName: 'Studio laptop',
    title: 'Morning light',
    tags: 'piano',
    language: 'en',
    seed: null,
    createdAt: '2026-11-14T12:00:00.000Z',
    requestedByMe: true,
    ...partial,
  };
}

export const page = <T,>(items: T[]): Page<T> => ({ items, page: 1, pageSize: 20, total: items.length, totalPages: items.length ? 1 : 0 });

export function workloadCommand(partial: Partial<CommandView> = {}): CommandView {
  return {
    id: 'c0000000-0000-4000-8000-000000000002',
    nodeId: 'ai-n2',
    type: 'localai.workload.activate',
    status: 'queued',
    params: { name: 'music' },
    source: 'ui',
    expiresAt: '2026-11-14T12:15:00.000Z',
    createdAt: '2026-11-14T12:00:00.000Z',
    claimedAt: null,
    finishedAt: null,
    error: null,
    ...partial,
  };
}
