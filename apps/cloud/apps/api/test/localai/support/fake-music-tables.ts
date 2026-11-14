// An in-memory stand-in for `localai_music_requests` and `localai_music_songs`, for the route tests over a mocked
// database: the few Prisma operations `MusicService` uses, with their real semantics (a conditional `updateMany` is
// atomic, `include` joins the songs and the node). The SQL itself is proven against PostgreSQL in `music.db.spec.ts`.
import { randomUUID } from 'node:crypto';
import type { LocalaiMusicRequest, LocalaiMusicSong } from '@prisma/client';

type Where = Record<string, unknown>;

function compare(actual: unknown, condition: unknown): boolean {
  if (condition !== null && typeof condition === 'object' && !(condition instanceof Date) && !Array.isArray(condition)) {
    return Object.entries(condition as Record<string, unknown>).every(([op, expected]) => {
      switch (op) {
        case 'in':
          return (expected as unknown[]).includes(actual);
        case 'notIn':
          return !(expected as unknown[]).includes(actual);
        case 'gte':
          return actual instanceof Date && actual.getTime() >= (expected as Date).getTime();
        default:
          throw new Error(`FakeMusicTables: unsupported operator ${op}`);
      }
    });
  }
  if (condition instanceof Date) return actual instanceof Date && actual.getTime() === condition.getTime();
  return actual === condition;
}

const matches = (row: object, where: Where = {}) =>
  Object.entries(where).every(([key, condition]) => compare((row as Record<string, unknown>)[key], condition));

export class FakeMusicTables {
  readonly requests: LocalaiMusicRequest[] = [];
  readonly songs: LocalaiMusicSong[] = [];
  /** Node row id to its lab id and name, for `include: { node }`. */
  readonly nodes = new Map<string, { nodeId: string; displayName: string }>();
  now = () => new Date();

  reset(): void {
    this.requests.length = 0;
    this.songs.length = 0;
  }

  private withRelations(row: LocalaiMusicRequest) {
    return {
      ...row,
      songs: this.songs.filter((song) => song.requestId === row.id).map((song) => ({ ...song })),
      node: this.nodes.get(row.nodeId) ?? { nodeId: 'ai-n0', displayName: '' },
    };
  }

  readonly localaiMusicRequest = {
    create: async ({ data }: { data: Record<string, any> }) => {
      const { songs, ...fields } = data;
      const now = this.now();
      const row = {
        commandId: null,
        requestedById: null,
        status: 'queued',
        seed: null,
        sampleMode: null,
        strength: null,
        sampleKey: null,
        sampleMime: null,
        sampleBytes: null,
        sampleSha256: null,
        errorCode: null,
        errorMessage: null,
        finishedAt: null,
        createdAt: now,
        updatedAt: now,
        id: randomUUID(),
        ...fields,
      } as LocalaiMusicRequest;
      this.requests.push(row);
      for (const song of (songs?.create ?? []) as Array<{ index: number; storageKey: string; status: LocalaiMusicSong['status'] }>) {
        this.songs.push({ id: randomUUID(), requestId: row.id, index: song.index, status: song.status, storageKey: song.storageKey, mime: null, sizeBytes: null, sha256: null, durationS: null, createdAt: now, readyAt: null });
      }
      return { ...row };
    },
    findUnique: async ({ where }: { where: { id?: string; commandId?: string } }) => {
      const row = this.requests.find((candidate) => (where.id !== undefined ? candidate.id === where.id : candidate.commandId === where.commandId));
      return row ? this.withRelations(row) : null;
    },
    findMany: async ({ where, skip, take }: { where?: Where; skip?: number; take?: number } = {}) =>
      this.requests
        .filter((row) => matches(row, where))
        .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime())
        .slice(skip ?? 0, take === undefined ? undefined : (skip ?? 0) + take)
        .map((row) => this.withRelations(row)),
    count: async ({ where }: { where?: Where } = {}) => this.requests.filter((row) => matches(row, where)).length,
    update: async ({ where, data }: { where: { id: string }; data: Partial<LocalaiMusicRequest> }) => {
      const row = this.requests.find((candidate) => candidate.id === where.id);
      if (!row) throw new Error('not found');
      Object.assign(row, data);
      return { ...row };
    },
    updateMany: async ({ where, data }: { where?: Where; data: Partial<LocalaiMusicRequest> }) => {
      let count = 0;
      for (const row of this.requests) {
        if (!matches(row, where)) continue;
        Object.assign(row, data);
        count += 1;
      }
      return { count };
    },
  };

  readonly localaiMusicSong = {
    findUnique: async ({ where }: { where: { id: string } }) => {
      const song = this.songs.find((candidate) => candidate.id === where.id);
      if (!song) return null;
      const request = this.requests.find((candidate) => candidate.id === song.requestId)!;
      return { ...song, request: this.withRelations(request) };
    },
    findMany: async ({ where, skip, take }: { where?: { status?: string; request?: { requestedById?: string; node?: { nodeId: string } } }; skip?: number; take?: number } = {}) =>
      this.songs
        .filter((song) => (where?.status === undefined || song.status === where.status))
        .map((song) => ({ ...song, request: this.withRelations(this.requests.find((candidate) => candidate.id === song.requestId)!) }))
        .filter((song) => (where?.request?.requestedById === undefined || song.request.requestedById === where.request.requestedById))
        .filter((song) => (where?.request?.node === undefined || song.request.node.nodeId === where.request.node.nodeId))
        .slice(skip ?? 0, take === undefined ? undefined : (skip ?? 0) + take),
    count: async ({ where }: { where?: { status?: string; requestId?: string; request?: unknown } } = {}) =>
      (await this.localaiMusicSong.findMany({ where: where as never })).filter((song) => where?.requestId === undefined || song.requestId === where.requestId).length,
    updateMany: async ({ where, data }: { where?: Where; data: Partial<LocalaiMusicSong> }) => {
      let count = 0;
      for (const song of this.songs) {
        if (!matches(song, where)) continue;
        Object.assign(song, data);
        count += 1;
      }
      return { count };
    },
    deleteMany: async ({ where }: { where?: Where } = {}) => {
      const before = this.songs.length;
      const keep = this.songs.filter((song) => !matches(song, where));
      this.songs.splice(0, this.songs.length, ...keep);
      return { count: before - keep.length };
    },
  };
}
