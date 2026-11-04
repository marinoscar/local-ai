// An in-memory stand-in for the `localai_commands` table and the two collaborators of the command plane (the event
// bus and the clock), for the unit tests of the state machine. It implements the few Prisma operations the services
// use with their real semantics: a conditional `updateMany` is atomic (one JS turn), and `create` enforces the
// partial unique index `localai_commands_active_dedup` and throws the P2002 Prisma raises for it. That the SQL
// itself behaves the same is proven against real PostgreSQL in `commands.db.spec.ts`.
import { randomUUID } from 'node:crypto';
import { Prisma, type LocalaiCommand } from '@prisma/client';

export class FakeClock {
  constructor(private current: Date = new Date('2026-11-02T15:00:00.000Z')) {}
  now(): Date {
    return new Date(this.current.getTime());
  }
  advance(ms: number): void {
    this.current = new Date(this.current.getTime() + ms);
  }
  set(date: Date): void {
    this.current = date;
  }
}

/** The event bus: records publishes and delivers to subscribers synchronously. */
export class FakeBus {
  readonly published: Array<{ channel: string; payload: unknown }> = [];
  private readonly handlers = new Map<string, Set<(payload: never, meta: never) => void>>();
  failPublish = false;

  async publish<T>(channel: string, payload: T): Promise<void> {
    if (this.failPublish) throw new Error('bus down');
    this.published.push({ channel, payload });
    for (const handler of this.handlers.get(channel) ?? []) handler(payload as never, {} as never);
  }

  subscribe<T>(channel: string, handler: (payload: T, meta: never) => void): () => void {
    const set = this.handlers.get(channel) ?? new Set();
    set.add(handler as never);
    this.handlers.set(channel, set);
    return () => set.delete(handler as never);
  }

  subscriberCount(channel: string): number {
    return this.handlers.get(channel)?.size ?? 0;
  }

  on(channel: string): unknown[] {
    return this.published.filter((entry) => entry.channel === channel).map((entry) => entry.payload);
  }
}

type Where = Record<string, unknown>;

function compare(actual: unknown, condition: unknown): boolean {
  if (condition !== null && typeof condition === 'object' && !(condition instanceof Date) && !Array.isArray(condition)) {
    const ops = condition as Record<string, unknown>;
    if (condition === Prisma.DbNull) return actual === null || actual === undefined;
    return Object.entries(ops).every(([op, expected]) => {
      switch (op) {
        case 'in':
          return (expected as unknown[]).includes(actual);
        case 'not':
          return expected === Prisma.DbNull ? actual !== null && actual !== undefined : !compare(actual, expected);
        case 'gt':
          return actual instanceof Date && actual.getTime() > (expected as Date).getTime();
        case 'gte':
          return actual instanceof Date && actual.getTime() >= (expected as Date).getTime();
        case 'lt':
          return actual instanceof Date && actual.getTime() < (expected as Date).getTime();
        case 'lte':
          return actual instanceof Date && actual.getTime() <= (expected as Date).getTime();
        case 'equals':
          return compare(actual, expected);
        default:
          throw new Error(`FakeCommandStore: unsupported operator ${op}`);
      }
    });
  }
  if (condition instanceof Date) return actual instanceof Date && actual.getTime() === condition.getTime();
  return actual === condition;
}

export class FakeCommandStore {
  readonly rows: LocalaiCommand[] = [];
  /** Operations run so far, in order, for assertions about "no write happened". */
  readonly writes: string[] = [];
  private clock: FakeClock;

  constructor(clock: FakeClock) {
    this.clock = clock;
  }

  private matches(row: LocalaiCommand, where: Where = {}): boolean {
    return Object.entries(where).every(([key, condition]) => compare((row as unknown as Record<string, unknown>)[key], condition));
  }

  /** The delegate the services see as `prisma.localaiCommand`. */
  readonly localaiCommand = {
    create: async ({ data }: { data: Partial<LocalaiCommand> & { nodeId: string; type: string; expiresAt: Date } }): Promise<LocalaiCommand> => {
      const dedupKey = data.dedupKey ?? null;
      if (dedupKey !== null && this.rows.some((row) => row.nodeId === data.nodeId && row.dedupKey === dedupKey && ['queued', 'running'].includes(row.status))) {
        throw new Prisma.PrismaClientKnownRequestError('Unique constraint failed', {
          code: 'P2002',
          clientVersion: 'test',
          meta: { driverAdapterError: { cause: { constraint: { fields: ['dedup_key'] }, originalMessage: 'localai_commands_active_dedup' } } },
        });
      }
      const row: LocalaiCommand = {
        id: randomUUID(),
        nodeId: data.nodeId,
        type: data.type,
        params: (data.params ?? {}) as LocalaiCommand['params'],
        status: (data.status ?? 'queued') as LocalaiCommand['status'],
        dedupKey,
        source: data.source ?? 'ui',
        requestedById: data.requestedById ?? null,
        attempts: 0,
        claimToken: null,
        leaseExpiresAt: null,
        claimedAt: null,
        expiresAt: data.expiresAt,
        finishedAt: null,
        result: null,
        error: null,
        traceparent: null,
        createdAt: this.clock.now(),
      };
      this.rows.push(row);
      this.writes.push('create');
      return { ...row };
    },
    findFirst: async ({ where, orderBy }: { where?: Where; orderBy?: { createdAt?: 'asc' | 'desc' } } = {}): Promise<LocalaiCommand | null> => {
      const found = this.rows.filter((row) => this.matches(row, where));
      if (orderBy?.createdAt === 'desc') found.reverse();
      return found[0] ? { ...found[0] } : null;
    },
    findMany: async ({ where, take }: { where?: Where; take?: number; select?: unknown; orderBy?: unknown } = {}): Promise<LocalaiCommand[]> =>
      this.rows.filter((row) => this.matches(row, where)).slice(0, take).map((row) => ({ ...row })),
    updateMany: async ({ where, data }: { where?: Where; data: Partial<LocalaiCommand> }): Promise<{ count: number }> => {
      let count = 0;
      for (const row of this.rows) {
        if (!this.matches(row, where)) continue;
        Object.assign(row, Object.fromEntries(Object.entries(data).map(([key, value]) => [key, (value as unknown) === Prisma.DbNull ? null : value])));
        count += 1;
      }
      if (count > 0) this.writes.push('updateMany');
      return { count };
    },
    deleteMany: async ({ where }: { where?: Where } = {}): Promise<{ count: number }> => {
      const keep = this.rows.filter((row) => !this.matches(row, where));
      const count = this.rows.length - keep.length;
      this.rows.splice(0, this.rows.length, ...keep);
      if (count > 0) this.writes.push('deleteMany');
      return { count };
    },
  };

  /** `prisma.localaiNode.update`, recorded. */
  readonly nodeUpdates: Array<{ id: string; data: Record<string, unknown> }> = [];
  /** Lab id (`ai-n3`) to row id, for `localaiNode.findUnique({ where: { nodeId } })`. */
  readonly knownNodes = new Map<string, string>();
  readonly localaiNode = {
    findUnique: async ({ where }: { where: { nodeId: string } }) => (this.knownNodes.has(where.nodeId) ? { id: this.knownNodes.get(where.nodeId) } : null),
    update: async ({ where, data }: { where: { id: string }; data: Record<string, unknown> }) => {
      this.nodeUpdates.push({ id: where.id, data });
      return {};
    },
  };

  /** Puts a row straight into the table (arranging a state), with sensible defaults. */
  seed(partial: Partial<LocalaiCommand> & { nodeId: string; type: string }): LocalaiCommand {
    const row: LocalaiCommand = {
      id: randomUUID(),
      params: {},
      status: 'queued',
      dedupKey: null,
      source: 'ui',
      requestedById: null,
      attempts: 0,
      claimToken: null,
      leaseExpiresAt: null,
      claimedAt: null,
      expiresAt: new Date(this.clock.now().getTime() + 15 * 60_000),
      finishedAt: null,
      result: null,
      error: null,
      traceparent: null,
      createdAt: this.clock.now(),
      ...partial,
    };
    this.rows.push(row);
    return row;
  }

  get(id: string): LocalaiCommand {
    const row = this.rows.find((candidate) => candidate.id === id);
    if (!row) throw new Error(`no row ${id}`);
    return row;
  }
}

/**
 * The claim over the in-memory table with the SAME rules as the SQL of `claim.sql.ts`: only this node's `queued`
 * rows that have not expired and are of a requested type, oldest first, none while the node has a `running` command
 * with an unexpired lease; the pick becomes `running` with a fresh token and the type's lease.
 */
export class StoreClaimRepository {
  constructor(private readonly store: FakeCommandStore) {}

  async claimNext(attempt: { nodeId: string; types: readonly string[]; now: Date; leaseMsOf(type: string): number }): Promise<LocalaiCommand | null> {
    const busy = this.store.rows.some(
      (row) => row.nodeId === attempt.nodeId && row.status === 'running' && row.leaseExpiresAt !== null && row.leaseExpiresAt.getTime() > attempt.now.getTime(),
    );
    if (busy) return null;
    const next = this.store.rows
      .filter((row) => row.nodeId === attempt.nodeId && row.status === 'queued' && row.expiresAt.getTime() > attempt.now.getTime() && attempt.types.includes(row.type))
      .sort((a, b) => a.createdAt.getTime() - b.createdAt.getTime())[0];
    if (!next) return null;
    next.status = 'running';
    next.attempts += 1;
    next.claimedAt = attempt.now;
    next.claimToken = randomUUID();
    next.leaseExpiresAt = new Date(attempt.now.getTime() + attempt.leaseMsOf(next.type));
    return { ...next };
  }
}
