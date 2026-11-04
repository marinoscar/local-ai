// The binding guard (docs/specs/cloud-app.md section 4.2) over an in-memory database: every decision of the spec's
// table, the pending to enrolled transition (once, even when two first calls race), the worker re-link after EAB
// pruned the worker node, and that no refusal or log line carries credential material. A real-SQL proof of the
// same flow is `test/localai/node-link.db.spec.ts`.
import { type ExecutionContext, ForbiddenException, Logger, NotFoundException, ServiceUnavailableException, UnauthorizedException } from '@nestjs/common';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';
import type { LocalaiNode, NodeCredential, WorkerNode } from '@prisma/client';

import { decideBinding } from './binding.decision';
import { AUDIT_NODE_BINDING_REFUSED, AUDIT_NODE_ENROLLED, LocalaiNodeBindingGuard, nodeBearerOf, nodeTokenDigest } from './binding.guard';
import type { NodeLinkRequest } from './node-link.types';

const OWNER = '11111111-1111-4111-8111-111111111111';
const STRANGER = '22222222-2222-4222-8222-222222222222';
const WORKER_N1 = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
const WORKER_N2 = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2';
const WORKER_N1_NEW = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3';
const CRED_N1 = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc1';
const CRED_N2 = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc2';
// Not real tokens: fixed strings with the `nod_` shape, so the tests can assert they never leak.
const TOKEN_N1 = `nod_${'1'.repeat(64)}`;
const TOKEN_N2 = `nod_${'2'.repeat(64)}`;

const worker = (id: string, name: string, createdById = OWNER): WorkerNode => ({ id, name, createdById, cliVersion: 'aiagent/0.1.0' }) as WorkerNode;
const credential = (id: string, token: string, extra: Partial<NodeCredential> = {}): NodeCredential =>
  ({ id, userId: OWNER, tokenHash: nodeTokenDigest(token), tokenPrefix: token.slice(0, 8), expiresAt: null, revokedAt: null, ...extra }) as NodeCredential;
const localaiNode = (nodeId: string, extra: Partial<LocalaiNode> = {}): LocalaiNode =>
  ({
    id: `row-${nodeId}`,
    nodeId,
    displayName: `Node ${nodeId}`,
    phase: 'pending',
    nodeCredentialId: null,
    workerNodeId: null,
    agentVersion: null,
    keyReportedAt: null,
    ...extra,
  }) as LocalaiNode;

/** A tiny in-memory stand-in for the four Prisma calls the guard makes, with updateMany's real conditional semantics. */
class FakeDb {
  credentials: NodeCredential[] = [];
  workers: WorkerNode[] = [];
  nodes: LocalaiNode[] = [];
  /** Hook run before each updateMany, to interleave a concurrent caller. */
  beforeUpdate?: () => Promise<void>;

  nodeCredential = { findUnique: async ({ where }: { where: { tokenHash: string } }) => this.credentials.find((c) => c.tokenHash === where.tokenHash) ?? null };
  workerNode = { findUnique: async ({ where }: { where: { id: string } }) => this.workers.find((w) => w.id === where.id) ?? null };
  localaiNode = {
    findUnique: async ({ where }: { where: { id?: string; nodeId?: string } }) =>
      this.nodes.find((n) => (where.id !== undefined ? n.id === where.id : n.nodeId === where.nodeId)) ?? null,
    updateMany: async ({ where, data }: { where: { id: string; phase: LocalaiNode['phase']; nodeCredentialId: string }; data: Partial<LocalaiNode> }) => {
      await this.beforeUpdate?.();
      const row = this.nodes.find((n) => n.id === where.id && n.phase === where.phase && n.nodeCredentialId === where.nodeCredentialId);
      if (!row) return { count: 0 };
      Object.assign(row, data);
      return { count: 1 };
    },
  };
}

interface Harness {
  db: FakeDb;
  audit: { record: jest.Mock };
  notifier: { nodeEnrolled: jest.Mock };
  guard: LocalaiNodeBindingGuard;
}

function harness(): Harness {
  const db = new FakeDb();
  const audit = { record: jest.fn().mockResolvedValue(undefined) };
  const notifier = { nodeEnrolled: jest.fn().mockResolvedValue(undefined) };
  // The guard only needs the four calls above; the full PrismaService type is not the point of this test.
  const guard = new LocalaiNodeBindingGuard(db as never, audit, notifier);
  return { db, audit, notifier, guard };
}

function requestOf(token: string | undefined, workerId: string, overrides: Partial<NodeLinkRequest> = {}): NodeLinkRequest {
  return {
    headers: token === undefined ? {} : { authorization: `Bearer ${token}` },
    params: { id: workerId },
    user: { id: OWNER },
    authCredential: { kind: 'node' },
    ...overrides,
  };
}

const contextOf = (request: NodeLinkRequest): ExecutionContext => ({ switchToHttp: () => ({ getRequest: () => request }) }) as unknown as ExecutionContext;

async function refusal(promise: Promise<unknown>): Promise<{ status: number; body: Record<string, unknown> }> {
  try {
    await promise;
  } catch (error) {
    const exception = error as ForbiddenException;
    return { status: exception.getStatus(), body: exception.getResponse() as Record<string, unknown> };
  }
  throw new Error('expected the guard to refuse');
}

/** Two nodes of one owner, each with its own credential and worker node. */
function twoNodes(h: Harness, phase: LocalaiNode['phase'] = 'pending'): void {
  h.db.credentials = [credential(CRED_N1, TOKEN_N1), credential(CRED_N2, TOKEN_N2)];
  h.db.workers = [worker(WORKER_N1, 'ai-n1'), worker(WORKER_N2, 'ai-n2')];
  h.db.nodes = [
    localaiNode('ai-n1', { nodeCredentialId: CRED_N1, phase }),
    localaiNode('ai-n2', { nodeCredentialId: CRED_N2, phase }),
  ];
}

describe('decideBinding (the pure part of spec section 4.2, steps 4 to 6)', () => {
  const base = { credentialId: CRED_N1, workerNodeId: WORKER_N1 };
  type Row = [string, Parameters<typeof decideBinding>[0], Record<string, unknown>];
  const rows: Row[] = [
    ['unknown Local AI node', { ...base, node: null }, { outcome: 'refuse', status: 404, details: { code: 'LAI-CLD-004', reason: 'unknown_localai_node' }, audit: false }],
    [
      'retired node',
      { ...base, node: { phase: 'retired', nodeCredentialId: CRED_N1, workerNodeId: WORKER_N1 } },
      { outcome: 'refuse', status: 403, details: { code: 'LAI-CLD-035', reason: 'node_retired' }, audit: false },
    ],
    [
      'no credential bound to the node',
      { ...base, node: { phase: 'pending', nodeCredentialId: null, workerNodeId: null } },
      { outcome: 'refuse', status: 403, details: { code: 'LAI-CLD-035', reason: 'credential_not_bound' }, audit: true },
    ],
    [
      'credential bound to another node',
      { ...base, node: { phase: 'enrolled', nodeCredentialId: CRED_N2, workerNodeId: WORKER_N1 } },
      { outcome: 'refuse', status: 403, details: { code: 'LAI-CLD-004', reason: 'credential_bound_to_other_node' }, audit: true },
    ],
    ['first contact of a pending node', { ...base, node: { phase: 'pending', nodeCredentialId: CRED_N1, workerNodeId: null } }, { outcome: 'admit', enroll: true, relink: true }],
    [
      'pending node whose worker id is already recorded',
      { ...base, node: { phase: 'pending', nodeCredentialId: CRED_N1, workerNodeId: WORKER_N1 } },
      { outcome: 'admit', enroll: true, relink: false },
    ],
    ['enrolled node, same worker', { ...base, node: { phase: 'enrolled', nodeCredentialId: CRED_N1, workerNodeId: WORKER_N1 } }, { outcome: 'admit', enroll: false, relink: false }],
    ['enrolled node registered again', { ...base, node: { phase: 'enrolled', nodeCredentialId: CRED_N1, workerNodeId: WORKER_N2 } }, { outcome: 'admit', enroll: false, relink: true }],
    [
      'a phase this code does not know',
      { ...base, node: { phase: 'archived' as never, nodeCredentialId: CRED_N1, workerNodeId: WORKER_N1 } },
      { outcome: 'refuse', status: 403, details: { code: 'LAI-CLD-035', reason: 'node_retired' }, audit: false },
    ],
  ];
  it.each(rows)('%s', (_name, input, expected) => {
    expect(decideBinding(input)).toEqual(expected);
  });
});

describe('LocalaiNodeBindingGuard', () => {
  beforeEach(() => {
    jest.spyOn(Logger.prototype, 'log').mockImplementation(() => undefined);
    jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
    jest.spyOn(Logger.prototype, 'error').mockImplementation(() => undefined);
  });
  afterEach(() => jest.restoreAllMocks());

  describe('step 1: only a node credential speaks for a node', () => {
    it.each<[string, Partial<NodeLinkRequest>]>([
      ['a browser session', { authCredential: { kind: 'jwt' } }],
      ['a personal access token', { authCredential: { kind: 'pat', tokenId: 'pat-1' } }],
      ['a request JwtAuthGuard never admitted', { authCredential: undefined }],
      ['a node credential without a user', { user: undefined }],
    ])('refuses %s with node_credential_required and reads no database row', async (_name, overrides) => {
      const h = harness();
      twoNodes(h);
      const spy = jest.spyOn(h.db.nodeCredential, 'findUnique');
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1, overrides))));
      expect(out.status).toBe(403);
      expect(out.body.details).toEqual({ code: 'LAI-CLD-004', reason: 'node_credential_required' });
      expect(spy).not.toHaveBeenCalled();
    });

    it.each([
      ['no Authorization header', undefined],
      ['a PAT-shaped bearer', 'pat_' + 'a'.repeat(64)],
      ['a JWT-shaped bearer', 'eyJhbGciOi.payload.signature'],
    ])('refuses an authCredential of kind node whose bearer is %s', async (_name, token) => {
      const h = harness();
      twoNodes(h);
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(token, WORKER_N1))));
      expect(out.body.details).toEqual({ code: 'LAI-CLD-004', reason: 'node_credential_required' });
    });
  });

  describe('step 2: the credential row', () => {
    it('answers 401 when the digest finds no credential (revoked and deleted alike)', async () => {
      const h = harness();
      twoNodes(h);
      h.db.credentials = [];
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).rejects.toBeInstanceOf(UnauthorizedException);
    });

    it('answers 401 for a credential revoked after EAB checked it', async () => {
      const h = harness();
      twoNodes(h);
      h.db.credentials[0] = credential(CRED_N1, TOKEN_N1, { revokedAt: new Date() });
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).rejects.toBeInstanceOf(UnauthorizedException);
    });

    it('answers 401 for an expired credential and admits one that expires in the future', async () => {
      const h = harness();
      twoNodes(h);
      h.db.credentials[0] = credential(CRED_N1, TOKEN_N1, { expiresAt: new Date(Date.now() - 1000) });
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).rejects.toBeInstanceOf(UnauthorizedException);
      h.db.credentials[0] = credential(CRED_N1, TOKEN_N1, { expiresAt: new Date(Date.now() + 60_000) });
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).resolves.toBe(true);
    });

    it('answers 401 when the credential belongs to a different user than the authenticated one', async () => {
      const h = harness();
      twoNodes(h);
      h.db.credentials[0] = credential(CRED_N1, TOKEN_N1, { userId: STRANGER });
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).rejects.toBeInstanceOf(UnauthorizedException);
    });
  });

  describe('step 3: the worker node in the URL', () => {
    it('answers 404 for an unknown worker node (pruned by EAB) and for an id that is not a uuid', async () => {
      const h = harness();
      twoNodes(h);
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb')))).rejects.toBeInstanceOf(NotFoundException);
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, "x'; drop table--")))).rejects.toBeInstanceOf(NotFoundException);
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, undefined as never)))).rejects.toBeInstanceOf(NotFoundException);
    });

    it("answers 403 for another owner's worker node", async () => {
      const h = harness();
      twoNodes(h);
      h.db.workers[0] = worker(WORKER_N1, 'ai-n1', STRANGER);
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).rejects.toBeInstanceOf(ForbiddenException);
      expect(h.audit.record).not.toHaveBeenCalled();
    });
  });

  describe('step 4: the Local AI node', () => {
    it('answers 404 unknown_localai_node when the worker name is not a known node id', async () => {
      const h = harness();
      twoNodes(h);
      h.db.workers.push(worker('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa9', 'ai-n9'));
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa9'))));
      expect(out.status).toBe(404);
      expect(out.body.details).toEqual({ code: 'LAI-CLD-004', reason: 'unknown_localai_node' });
    });

    it('answers 403 LAI-CLD-035 for a retired node, even with its own credential, and writes nothing', async () => {
      const h = harness();
      twoNodes(h, 'retired');
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1))));
      expect(out.status).toBe(403);
      expect(out.body.details).toEqual({ code: 'LAI-CLD-035', reason: 'node_retired' });
      expect(h.db.nodes[0].phase).toBe('retired');
      expect(h.audit.record).not.toHaveBeenCalled();
    });
  });

  describe('step 5: one credential, one node', () => {
    it('refuses the credential of ai-n1 on ai-n2 and writes exactly one binding_refused audit row', async () => {
      const h = harness();
      twoNodes(h, 'enrolled');
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N2))));
      expect(out.status).toBe(403);
      expect(out.body.details).toEqual({ code: 'LAI-CLD-004', reason: 'credential_bound_to_other_node' });
      expect(h.audit.record).toHaveBeenCalledTimes(1);
      expect(h.audit.record).toHaveBeenCalledWith({
        action: AUDIT_NODE_BINDING_REFUSED,
        actorUserId: OWNER,
        targetType: 'localai_node',
        targetId: 'ai-n2',
        meta: { credentialPrefix: TOKEN_N1.slice(0, 8), claimedNodeId: 'ai-n2' },
      });
      // The victim node is untouched.
      expect(h.db.nodes[1]).toMatchObject({ phase: 'enrolled', workerNodeId: null, nodeCredentialId: CRED_N2 });
    });

    it('refuses a pending node that has no credential bound yet (fail closed) and audits it', async () => {
      const h = harness();
      twoNodes(h);
      h.db.nodes[1].nodeCredentialId = null;
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N2))));
      expect(out.body.details).toEqual({ code: 'LAI-CLD-035', reason: 'credential_not_bound' });
      expect(h.audit.record).toHaveBeenCalledTimes(1);
      expect(h.db.nodes[1]).toMatchObject({ phase: 'pending', workerNodeId: null });
    });

    it('still refuses when the audit sink fails', async () => {
      const h = harness();
      twoNodes(h, 'enrolled');
      h.audit.record.mockRejectedValue(new Error('database down'));
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N2))));
      expect(out.body.details).toEqual({ code: 'LAI-CLD-004', reason: 'credential_bound_to_other_node' });
    });
  });

  describe('step 6: enrolling and re-linking', () => {
    it('enrolls a pending node on its first call: phase, worker id, one audit row, one notification', async () => {
      const h = harness();
      twoNodes(h);
      const request = requestOf(TOKEN_N1, WORKER_N1);
      await expect(h.guard.canActivate(contextOf(request))).resolves.toBe(true);
      expect(h.db.nodes[0]).toMatchObject({ phase: 'enrolled', workerNodeId: WORKER_N1 });
      expect(request.localaiNode).toMatchObject({ nodeId: 'ai-n1', phase: 'enrolled', workerNodeId: WORKER_N1 });
      expect(h.audit.record).toHaveBeenCalledTimes(1);
      expect(h.audit.record).toHaveBeenCalledWith({
        action: AUDIT_NODE_ENROLLED,
        actorUserId: OWNER,
        targetType: 'localai_node',
        targetId: 'ai-n1',
        meta: { workerNodeId: WORKER_N1, agentVersion: 'aiagent/0.1.0' },
      });
      expect(h.notifier.nodeEnrolled).toHaveBeenCalledTimes(1);
      expect(h.notifier.nodeEnrolled).toHaveBeenCalledWith({ nodeId: 'ai-n1', displayName: 'Node ai-n1', workerNodeId: WORKER_N1 });
    });

    it('does not enroll twice: later calls write and announce nothing', async () => {
      const h = harness();
      twoNodes(h);
      await h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)));
      const update = jest.spyOn(h.db.localaiNode, 'updateMany');
      await h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)));
      await h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)));
      expect(update).not.toHaveBeenCalled();
      expect(h.audit.record).toHaveBeenCalledTimes(1);
      expect(h.notifier.nodeEnrolled).toHaveBeenCalledTimes(1);
    });

    it('two concurrent first calls produce one audit row and one notification', async () => {
      const h = harness();
      twoNodes(h);
      // Hold the first writer until the second has also read the row as pending, then release both.
      let release!: () => void;
      const gate = new Promise<void>((resolve) => (release = resolve));
      let writers = 0;
      h.db.beforeUpdate = async () => {
        writers += 1;
        if (writers === 2) release();
        await gate;
      };
      const results = await Promise.all([
        h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1))),
        h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1))),
      ]);
      expect(results).toEqual([true, true]);
      expect(h.audit.record).toHaveBeenCalledTimes(1);
      expect(h.notifier.nodeEnrolled).toHaveBeenCalledTimes(1);
      expect(h.db.nodes[0]).toMatchObject({ phase: 'enrolled', workerNodeId: WORKER_N1 });
    });

    it('re-links the worker id after the node registered again, without a second enrollment', async () => {
      const h = harness();
      twoNodes(h);
      await h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)));
      // EAB pruned worker N1; the agent registered again and got a new worker node id.
      h.db.workers = [worker(WORKER_N1_NEW, 'ai-n1'), worker(WORKER_N2, 'ai-n2')];
      const request = requestOf(TOKEN_N1, WORKER_N1_NEW);
      await expect(h.guard.canActivate(contextOf(request))).resolves.toBe(true);
      expect(h.db.nodes[0]).toMatchObject({ phase: 'enrolled', workerNodeId: WORKER_N1_NEW });
      expect(request.localaiNode?.workerNodeId).toBe(WORKER_N1_NEW);
      expect(h.audit.record).toHaveBeenCalledTimes(1);
      expect(h.notifier.nodeEnrolled).toHaveBeenCalledTimes(1);
    });

    it('the old worker id is gone after the prune: it answers 404 and never reaches the node row', async () => {
      const h = harness();
      twoNodes(h);
      h.db.workers = [worker(WORKER_N1_NEW, 'ai-n1')];
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).rejects.toBeInstanceOf(NotFoundException);
    });

    it('refuses when the node is retired between the read and the write, and enrolls nothing', async () => {
      const h = harness();
      twoNodes(h);
      h.db.beforeUpdate = async () => {
        h.db.nodes[0].phase = 'retired';
      };
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1))));
      expect(out.body.details).toEqual({ code: 'LAI-CLD-035', reason: 'node_retired' });
      expect(h.audit.record).not.toHaveBeenCalled();
      expect(h.notifier.nodeEnrolled).not.toHaveBeenCalled();
    });

    it('refuses when the credential is rebound between the read and the write', async () => {
      const h = harness();
      twoNodes(h);
      h.db.beforeUpdate = async () => {
        h.db.nodes[0].nodeCredentialId = CRED_N2;
      };
      const out = await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1))));
      expect(out.body.details).toEqual({ code: 'LAI-CLD-004', reason: 'credential_bound_to_other_node' });
      expect(h.notifier.nodeEnrolled).not.toHaveBeenCalled();
    });

    it('gives up with 503 (never admits) when the row keeps changing under the write', async () => {
      const h = harness();
      twoNodes(h);
      h.db.beforeUpdate = async () => {
        // Flip the phase back and forth so the conditional write never matches.
        h.db.nodes[0].phase = h.db.nodes[0].phase === 'pending' ? 'enrolled' : 'pending';
        h.db.nodes[0].workerNodeId = null;
      };
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).rejects.toBeInstanceOf(ServiceUnavailableException);
    });

    it('keeps the call admitted when the audit sink and the notifier both fail after the write', async () => {
      const h = harness();
      twoNodes(h);
      h.audit.record.mockRejectedValue(new Error('audit down'));
      h.notifier.nodeEnrolled.mockRejectedValue(new Error('notifier down'));
      await expect(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1)))).resolves.toBe(true);
      expect(h.db.nodes[0].phase).toBe('enrolled');
    });
  });

  describe('step 7: a clean call', () => {
    it('attaches the verified row for an enrolled node', async () => {
      const h = harness();
      twoNodes(h, 'enrolled');
      h.db.nodes[0].workerNodeId = WORKER_N1;
      const request = requestOf(TOKEN_N1, WORKER_N1);
      await expect(h.guard.canActivate(contextOf(request))).resolves.toBe(true);
      expect(request.localaiNode?.nodeId).toBe('ai-n1');
    });
  });

  describe('credential material', () => {
    it('never appears in a refusal body or a log line', async () => {
      const h = harness();
      twoNodes(h, 'enrolled');
      const logged: string[] = [];
      for (const level of ['log', 'warn', 'error'] as const) {
        jest.spyOn(Logger.prototype, level).mockImplementation((...args: unknown[]) => void logged.push(args.map(String).join(' ')));
      }
      const outcomes = [
        await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N2)))),
        await refusal(h.guard.canActivate(contextOf(requestOf(TOKEN_N1, WORKER_N1, { authCredential: { kind: 'jwt' } })))),
      ];
      const everything = JSON.stringify(outcomes) + logged.join('\n') + JSON.stringify(h.audit.record.mock.calls);
      expect(everything).not.toContain(TOKEN_N1);
      expect(everything).not.toContain(nodeTokenDigest(TOKEN_N1));
      expect(everything).not.toMatch(/[0-9a-f]{40,}/);
    });
  });
});

describe('the bearer digest', () => {
  it('reads only a `Bearer nod_` header', () => {
    expect(nodeBearerOf(`Bearer ${TOKEN_N1}`)).toBe(TOKEN_N1);
    expect(nodeBearerOf(`bearer ${TOKEN_N1}`)).toBeNull();
    expect(nodeBearerOf(TOKEN_N1)).toBeNull();
    expect(nodeBearerOf(`Bearer pat_${'a'.repeat(8)}`)).toBeNull();
    expect(nodeBearerOf(undefined)).toBeNull();
    expect(nodeBearerOf([`Bearer ${TOKEN_N1}`])).toBeNull();
  });

  // EAB's `hash` is private. Pin our re-derivation to the real service: mint a credential through
  // `NodeCredentialService`, then find it again by the digest computed here (and let the service itself validate it).
  it('matches the digest NodeCredentialService stores, so a minted credential is found by the re-derived hash', async () => {
    const rows: Array<Record<string, unknown>> = [];
    const prisma = {
      nodeCredential: {
        create: async ({ data }: { data: Record<string, unknown> }) => {
          const row = { id: CRED_N1, createdAt: new Date(), lastUsedAt: null, revokedAt: null, ...data };
          rows.push(row);
          return row;
        },
        findUnique: async ({ where }: { where: { tokenHash: string } }) => {
          const row = rows.find((r) => r.tokenHash === where.tokenHash);
          return row ? { ...row, user: { isActive: true, id: OWNER, userRoles: [], memberships: [] } } : null;
        },
        update: async () => ({}),
      },
    };
    jest.spyOn(Logger.prototype, 'log').mockImplementation(() => undefined);
    const service = new NodeCredentialService(prisma as never);
    const minted = await service.createCredential(OWNER, { name: 'test node' } as never);

    expect(minted.token).toMatch(/^nod_[0-9a-f]{64}$/);
    const found = await prisma.nodeCredential.findUnique({ where: { tokenHash: nodeTokenDigest(minted.token) } });
    expect(found).not.toBeNull();
    expect((found as { id: string } | null)?.id).toBe(minted.id);
    expect(rows[0].tokenHash).toBe(nodeTokenDigest(minted.token));
    // And the service itself accepts the token through the same digest.
    await expect(service.validateToken(minted.token)).resolves.not.toBeNull();
    jest.restoreAllMocks();
  });
});
