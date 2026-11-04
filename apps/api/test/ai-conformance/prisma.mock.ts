// A deep mock of the app's Prisma client, for the AI conformance suites: they
// boot the whole AppModule over a MOCKED database (no PostgreSQL), because the
// AI runtime is replaced by the platform's in-memory harness and the suites only
// care whether a request is routed, guarded and answered. A mock is the only way
// they can stub the few rows the real providers read (`prismaMock.userAiKey.
// findMany.mockResolvedValue([...])`). Whether row-level security and the real
// queries hold is the db tier's job (`*.db.spec.ts`).
import { PrismaClient } from '@prisma/client';
import { mockDeep, mockReset, type DeepMockProxy } from 'jest-mock-extended';

/** The single-mode default organization every mocked user belongs to. */
export const MOCK_DEFAULT_ORG_ID = '99999999-9999-4999-8999-999999999999';

type MockPrisma = DeepMockProxy<PrismaClient>;

const base: MockPrisma = mockDeep<PrismaClient>();

/**
 * Gives the mock the org-scope entry points of `PrismaService`: `forUser` and
 * `forOrg` return the mock itself, and `runInOrg(orgId, fn)` runs `fn` with the
 * mock as the transaction client, so a stub on `userAiKey.findMany` is what the
 * code under test reads. Defined once, as plain functions, so `mockReset` cannot
 * wipe them.
 */
const scoped: Record<string, unknown> = {
  forUser: () => proxy,
  forOrg: () => proxy,
  runInOrg: (_orgId: string, fn: (tx: unknown) => unknown) => fn(proxy),
  asSystem: () => proxy,
  runAsSystem: (_reason: string, fn: (tx: unknown) => unknown) => fn(proxy),
};

const proxy: MockPrisma = new Proxy(base, {
  get(target, prop, receiver) {
    if (typeof prop === 'string' && prop in scoped) return scoped[prop];
    return Reflect.get(target, prop, receiver);
  },
}) as MockPrisma;

/** The mock. Typed loosely on purpose: a spec stubs partial rows. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const prismaMock = proxy as any;

/** Clears every stub and recorded call. */
export function resetPrismaMock(): void {
  mockReset(base);
}
