// Fake transports for the component tests: every call is recorded and every answer is driven by the test.
import { TransportError } from '../common/errors';
import type { GenerateAccepted, GenerateRequest, MusicOp, MusicTransport, Song, SongsPage } from '../music/types';
import type { ActivateOptions, WorkloadsResponse, WorkloadTransport } from '../workloads/types';

export interface Deferred<T> {
  promise: Promise<T>;
  resolve: (v: T) => void;
  reject: (e: unknown) => void;
}

export function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

export function err(code: string, message = '', fix = '', status = 409): TransportError {
  return new TransportError({ code, message, fix, status });
}

export const WORKLOADS: WorkloadsResponse = {
  active: 'coding',
  workloads: [
    { name: 'coding', label: { en: 'Coding', es: 'Programación' }, models: ['coder-a'], active: true, fits: true, missing: [] },
    { name: 'chat', label: { en: 'Chat', es: 'Conversación' }, models: ['chat-a'], active: false, fits: true, missing: [] },
    { name: 'music', label: { en: 'Music', es: 'Música' }, models: ['music-a'], active: false, fits: true, missing: [] },
    { name: 'video', label: { en: 'Video', es: 'Video' }, models: ['video-a'], active: false, fits: false, missing: ['video-a'] },
  ],
};

export class FakeWorkloads implements WorkloadTransport {
  data: WorkloadsResponse = structuredClone(WORKLOADS);
  listError: unknown = null;
  calls: { name: string; opts?: ActivateOptions }[] = [];
  pending: Deferred<void> | null = null;

  async list(): Promise<WorkloadsResponse> {
    if (this.listError) throw this.listError;
    return structuredClone(this.data);
  }

  activate(name: string, opts?: ActivateOptions): Promise<void> {
    this.calls.push({ name, opts });
    this.pending = deferred<void>();
    return this.pending.promise;
  }

  /** Finishes the pending activation and makes `name` the active workload. */
  succeed(name: string) {
    this.data.active = name;
    for (const w of this.data.workloads) w.active = w.name === name;
    this.pending?.resolve();
  }

  progress(p: number | null, step?: string) {
    this.calls.at(-1)?.opts?.onProgress?.({ progress: p, step });
  }
}

export function song(over: Partial<Song> = {}): Song {
  return {
    id: 'song-1',
    created_at: '2026-11-14T10:00:00Z',
    title: 'Morning light',
    tags: 'acoustic, calm',
    language: 'en',
    duration_s: 65,
    model_id: 'music-a',
    seed: 7,
    mime: 'audio/wav',
    size_bytes: 1000,
    origin: 'local',
    ...over,
  };
}

export class FakeMusic implements MusicTransport {
  songs: Song[] = [];
  pages: SongsPage[] | null = null;
  listError: unknown = null;
  generated: GenerateRequest[] = [];
  generateResult: Deferred<GenerateAccepted> | null = null;
  watchers = new Map<string, (op: MusicOp) => void>();
  stopped: string[] = [];
  cancelled: string[] = [];
  cancelError: unknown = null;
  deleted: string[] = [];
  deleteResult: Deferred<void> | null = null;
  listCalls: { cursor?: string | null; limit?: number }[] = [];

  async status() {
    return { installed: true, loaded_model: 'music-a', engine_state: 'ready' as const, queue: { running: 0, queued: 0, max: 4 } };
  }

  generate(req: GenerateRequest): Promise<GenerateAccepted> {
    this.generated.push(req);
    this.generateResult = deferred<GenerateAccepted>();
    return this.generateResult.promise;
  }

  watch(opId: string, onUpdate: (op: MusicOp) => void): () => void {
    this.watchers.set(opId, onUpdate);
    return () => {
      this.stopped.push(opId);
      this.watchers.delete(opId);
    };
  }

  /** Sends an update of op `opId` to its watcher. */
  emit(opId: string, op: Partial<MusicOp>) {
    this.watchers.get(opId)?.({ op_id: opId, state: 'running', progress: null, song_ids: [], error: null, ...op });
  }

  async cancel(opId: string): Promise<void> {
    this.cancelled.push(opId);
    if (this.cancelError) throw this.cancelError;
  }

  async listSongs(opts?: { cursor?: string | null; limit?: number }): Promise<SongsPage> {
    this.listCalls.push(opts ?? {});
    if (this.listError) throw this.listError;
    if (this.pages) {
      const i = opts?.cursor ? Number(opts.cursor) : 0;
      return this.pages[i] ?? { songs: [], next_cursor: null };
    }
    return { songs: this.songs, next_cursor: null };
  }

  audioUrl(s: Song): string {
    return `/api/v1/music/songs/${s.id}/audio`;
  }

  deleteSong(id: string): Promise<void> {
    this.deleted.push(id);
    this.deleteResult = deferred<void>();
    return this.deleteResult.promise;
  }
}
