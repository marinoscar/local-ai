// StateService over a fake transaction client: the order of checks, what is written, what is published after the
// commit, and that no refusal echoes a value from the body. The same flow on real SQL is
// `test/localai/node-link-state.db.spec.ts`.
import { BadRequestException, ConflictException, ForbiddenException, Logger } from '@nestjs/common';
import type { LocalaiNode } from '@prisma/client';

import { ai3, secrets } from '../../../test/localai/state-fixtures';
import { StateService, LOCALAI_NODE_STATE_CHANNEL } from './state.service';
import { toModelFields } from './state.projection';

// The service logs every decision; keep the test output readable (the log test below spies on the instance).
Logger.overrideLogger(false);

const NODE = { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3', nodeId: 'ai-n3' } as LocalaiNode;
const USER = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb3';

interface Rig {
  service: StateService;
  tx: ReturnType<typeof makeTx>;
  prisma: { $transaction: jest.Mock };
  bus: { publish: jest.Mock };
  audit: { record: jest.Mock };
  alerts: { reevaluateNode: jest.Mock };
  calls: string[];
}

function makeTx(row: Record<string, unknown> | null, existingModels: unknown[]) {
  return {
    localaiNode: {
      updateMany: jest.fn(async (..._args: unknown[]) => ({ count: row ? 1 : 0 })),
      findUnique: jest.fn(async (..._args: unknown[]) => row),
      update: jest.fn(async (..._args: unknown[]) => ({})),
    },
    localaiNodeModel: {
      findMany: jest.fn(async (..._args: unknown[]) => existingModels),
      deleteMany: jest.fn(async (..._args: unknown[]) => ({ count: 0 })),
      createMany: jest.fn(async (..._args: unknown[]) => ({ count: 0 })),
      update: jest.fn(async (..._args: unknown[]) => ({})),
    },
  };
}

function rig(options: { row?: Record<string, unknown> | null; models?: unknown[]; failPublish?: boolean; failAudit?: boolean; failAlerts?: boolean } = {}): Rig {
  const row = options.row === undefined ? { phase: 'enrolled', mode: null, lastState: null, lastStateSeq: null } : options.row;
  const tx = makeTx(row, options.models ?? []);
  const calls: string[] = [];
  const prisma = {
    $transaction: jest.fn(async (fn: (client: typeof tx) => Promise<unknown>) => {
      const result = await fn(tx);
      calls.push('commit');
      return result;
    }),
  };
  const bus = {
    publish: jest.fn(async (..._args: unknown[]) => {
      calls.push('publish');
      if (options.failPublish) throw new Error('bus down');
    }),
  };
  const audit = {
    record: jest.fn(async (..._args: unknown[]) => {
      calls.push('audit');
      if (options.failAudit) throw new Error('audit down');
    }),
  };
  const alerts = {
    reevaluateNode: jest.fn(async (..._args: unknown[]) => {
      calls.push('alerts');
      if (options.failAlerts) throw new Error('alerts down');
    }),
  };
  const service = new StateService(prisma as never, audit as never, bus as never, alerts as never);
  return { service, tx, prisma, bus, audit, alerts, calls };
}

const modelRows = (snapshot = ai3()) => snapshot.models.map((model) => ({ modelId: model.id, ...toModelFields(model) }));

describe('StateService.ingest: acceptance', () => {
  it('accepts the full ai-n3 fixture, stores it verbatim and projects the models', async () => {
    const r = rig();
    const snapshot = ai3();
    await expect(r.service.ingest(NODE, USER, snapshot)).resolves.toEqual({ accepted: true, seq: snapshot.seq });

    const update = r.tx.localaiNode.update.mock.calls[0]![0] as { where: unknown; data: Record<string, unknown> };
    expect(update.where).toEqual({ id: NODE.id });
    expect(update.data).toMatchObject({
      lastState: snapshot,
      lastStateSeq: BigInt(snapshot.seq),
      mode: 'online',
      connectivity: 'full',
      profile: 'large-24gb',
      agentVersion: '0.4.0',
      catalogVersion: '2026.11.02-1',
      backend: 'cuda',
    });
    expect(update.data.lastStateAt).toBeInstanceOf(Date);
    const created = (r.tx.localaiNodeModel.createMany.mock.calls[0]![0] as { data: Array<{ nodeId: string; modelId: string }> }).data;
    expect(created.map((row) => row.modelId)).toEqual(snapshot.models.map((model) => model.id));
    expect(created.every((row) => row.nodeId === NODE.id)).toBe(true);
    expect(r.tx.localaiNodeModel.deleteMany).not.toHaveBeenCalled();
    expect(r.tx.localaiNodeModel.update).not.toHaveBeenCalled();
  });

  it('takes the row lock (an UPDATE of the node row) before reading the stored state', async () => {
    const r = rig();
    const order: string[] = [];
    r.tx.localaiNode.updateMany.mockImplementation(async () => {
      order.push('lock');
      return { count: 1 };
    });
    r.tx.localaiNode.findUnique.mockImplementation(async () => {
      order.push('read');
      return { phase: 'enrolled', mode: null, lastState: null, lastStateSeq: null };
    });
    await r.service.ingest(NODE, USER, ai3());
    expect(order).toEqual(['lock', 'read']);
    expect(r.tx.localaiNode.updateMany).toHaveBeenCalledWith({ where: { id: NODE.id }, data: { updatedAt: expect.any(Date) } });
  });

  it('publishes localai.node.state exactly once, AFTER the commit, with the flags', async () => {
    const r = rig();
    await r.service.ingest(NODE, USER, ai3());
    expect(r.bus.publish).toHaveBeenCalledTimes(1);
    expect(r.bus.publish).toHaveBeenCalledWith(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n3', modelsChanged: true, modeChanged: false });
    expect(LOCALAI_NODE_STATE_CHANNEL).toBe('localai.node.state');
    expect(r.calls).toEqual(['commit', 'publish']);
  });

  it('modelsChanged is false when the projection did not change', async () => {
    const r = rig({ models: modelRows(), row: { phase: 'enrolled', mode: 'online', lastState: { boot_id: 'x' }, lastStateSeq: 1n } });
    await r.service.ingest(NODE, USER, ai3());
    expect(r.bus.publish).toHaveBeenCalledWith(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n3', modelsChanged: false, modeChanged: false });
    expect(r.tx.localaiNodeModel.createMany).not.toHaveBeenCalled();
    expect(r.tx.localaiNodeModel.update).not.toHaveBeenCalled();
    expect(r.tx.localaiNodeModel.deleteMany).not.toHaveBeenCalled();
  });

  it('deletes an absent model, updates a changed one and creates a new one', async () => {
    const stored = modelRows();
    stored.push({ ...stored[0]!, modelId: 'removed-model' });
    stored[1] = { ...stored[1]!, status: 'loaded' };
    const r = rig({ models: stored.filter((row) => row.modelId !== 'embeddinggemma-300m-q8') });
    await r.service.ingest(NODE, USER, ai3());
    expect(r.tx.localaiNodeModel.deleteMany).toHaveBeenCalledWith({ where: { nodeId: NODE.id, modelId: { in: ['removed-model'] } } });
    expect(r.tx.localaiNodeModel.update).toHaveBeenCalledTimes(1);
    expect(r.tx.localaiNodeModel.update.mock.calls[0]![0]).toMatchObject({ where: { nodeId_modelId: { nodeId: NODE.id, modelId: ai3().models[1]!.id } } });
    expect((r.tx.localaiNodeModel.createMany.mock.calls[0]![0] as { data: unknown[] }).data).toHaveLength(1);
    expect(r.bus.publish).toHaveBeenCalledWith(LOCALAI_NODE_STATE_CHANNEL, expect.objectContaining({ modelsChanged: true }));
  });

  it('keeps the previous ready of a root whose new ready is null in the stored lastState', async () => {
    const previous = ai3();
    const r = rig({ row: { phase: 'enrolled', mode: 'online', lastState: previous, lastStateSeq: 41n } });
    const next = ai3();
    next.seq = 42;
    next.ark.roots[0]!.ready = null;
    await r.service.ingest(NODE, USER, next);
    const stored = (r.tx.localaiNode.update.mock.calls[0]![0] as { data: { lastState: ReturnType<typeof ai3> } }).data.lastState;
    expect(stored.ark.roots[0]!.ready).toEqual(previous.ark.roots[0]!.ready);
    expect(stored.seq).toBe(42);
  });
});

describe('StateService.ingest: mode change', () => {
  it('audits localai.node.mode_changed with { from, to }, re-evaluates alerts and flags the event', async () => {
    const r = rig({ row: { phase: 'enrolled', mode: 'online', lastState: { boot_id: 'x' }, lastStateSeq: 1n }, models: modelRows() });
    const snapshot = ai3();
    snapshot.mode = 'offline';
    await r.service.ingest(NODE, USER, snapshot);
    expect(r.bus.publish).toHaveBeenCalledWith(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n3', modelsChanged: false, modeChanged: true });
    expect(r.audit.record).toHaveBeenCalledTimes(1);
    expect(r.audit.record).toHaveBeenCalledWith({
      action: 'localai.node.mode_changed',
      actorUserId: USER,
      targetType: 'localai_node',
      targetId: 'ai-n3',
      meta: { from: 'online', to: 'offline' },
    });
    expect(r.alerts.reevaluateNode).toHaveBeenCalledWith('ai-n3');
    expect(r.calls).toEqual(['commit', 'publish', 'audit', 'alerts']);
  });

  it('does not audit or re-evaluate when the mode is unchanged or on the very first snapshot', async () => {
    const same = rig({ row: { phase: 'enrolled', mode: 'online', lastState: null, lastStateSeq: null } });
    await same.service.ingest(NODE, USER, ai3());
    const first = rig();
    await first.service.ingest(NODE, USER, ai3());
    for (const r of [same, first]) {
      expect(r.audit.record).not.toHaveBeenCalled();
      expect(r.alerts.reevaluateNode).not.toHaveBeenCalled();
    }
  });

  it('a failing bus, audit or alert hook never fails the accepted snapshot', async () => {
    const r = rig({ row: { phase: 'enrolled', mode: 'online', lastState: null, lastStateSeq: null }, failPublish: true, failAudit: true, failAlerts: true });
    const snapshot = ai3();
    snapshot.mode = 'survival';
    await expect(r.service.ingest(NODE, USER, snapshot)).resolves.toEqual({ accepted: true, seq: snapshot.seq });
    expect(r.audit.record).toHaveBeenCalled();
    expect(r.alerts.reevaluateNode).toHaveBeenCalled();
  });
});

describe('StateService.ingest: ordering', () => {
  it('refuses the same boot with a seq that is not newer: 409 stale_seq, nothing written or published', async () => {
    const r = rig({ row: { phase: 'enrolled', mode: 'online', lastState: { boot_id: ai3().boot_id }, lastStateSeq: BigInt(ai3().seq) } });
    const error = await r.service.ingest(NODE, USER, ai3()).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ConflictException);
    expect((error as ConflictException).getResponse()).toMatchObject({ details: { code: 'LAI-CLD-008', reason: 'stale_seq' } });
    expect(r.tx.localaiNode.update).not.toHaveBeenCalled();
    expect(r.tx.localaiNodeModel.findMany).not.toHaveBeenCalled();
    expect(r.bus.publish).not.toHaveBeenCalled();
    expect(r.audit.record).not.toHaveBeenCalled();
  });

  it('accepts a new boot_id with seq 0', async () => {
    const r = rig({ row: { phase: 'enrolled', mode: 'online', lastState: { boot_id: 'old-boot' }, lastStateSeq: 900n } });
    const snapshot = ai3();
    snapshot.seq = 0;
    await expect(r.service.ingest(NODE, USER, snapshot)).resolves.toEqual({ accepted: true, seq: 0 });
    expect(r.tx.localaiNode.update).toHaveBeenCalled();
  });

  it('refuses a snapshot for a node that was retired while it was in flight (403 LAI-CLD-035)', async () => {
    const r = rig({ row: { phase: 'retired', mode: 'online', lastState: null, lastStateSeq: null } });
    const error = await r.service.ingest(NODE, USER, ai3()).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ForbiddenException);
    expect((error as ForbiddenException).getResponse()).toMatchObject({ details: { code: 'LAI-CLD-035', reason: 'node_retired' } });
    expect(r.tx.localaiNode.update).not.toHaveBeenCalled();
  });

  it('a deleted node row is a 404', async () => {
    const r = rig({ row: null });
    await expect(r.service.ingest(NODE, USER, ai3())).rejects.toMatchObject({ status: 404 });
  });
});

describe('StateService.ingest: refusals happen before any database access', () => {
  async function refusal(body: unknown) {
    const r = rig();
    const error = await r.service.ingest(NODE, USER, body).catch((e: unknown) => e);
    expect(r.prisma.$transaction).not.toHaveBeenCalled();
    expect(r.bus.publish).not.toHaveBeenCalled();
    return error as BadRequestException;
  }

  it('a node_id that is not the bound node: 400 LAI-CLD-004', async () => {
    const snapshot = ai3();
    snapshot.node_id = 'ai-n4';
    const error = await refusal(snapshot);
    expect(error).toBeInstanceOf(BadRequestException);
    expect(error.getResponse()).toMatchObject({ details: { code: 'LAI-CLD-004', reason: 'node_id_mismatch' } });
  });

  it('201 models: 400 with a schema issue at models', async () => {
    const snapshot = ai3();
    snapshot.models = Array.from({ length: 201 }, (_, index) => ({ ...snapshot.models[0]!, id: `model-${index}` }));
    const error = await refusal(snapshot);
    expect(error.getResponse()).toMatchObject({ details: { code: 'LAI-CLD-008', reason: 'schema', issues: [{ path: 'models', code: 'too_big' }] } });
  });

  it('an unknown key anywhere: 400, naming the object path and never the key', async () => {
    const snapshot = ai3() as unknown as Record<string, Record<string, unknown>>;
    snapshot.llama!.surprise_field = 1;
    const error = await refusal(snapshot);
    const body = error.getResponse() as { details: { issues: Array<{ path: string; code: string }> } };
    expect(body.details.issues).toEqual([{ path: 'llama', code: 'unrecognized_keys' }]);
    expect(JSON.stringify(body)).not.toContain('surprise_field');
  });

  it('a body that is not an object: 400', async () => {
    for (const body of [null, 'text', 5, [], undefined]) {
      const error = await refusal(body);
      expect(error.getResponse()).toMatchObject({ details: { reason: 'schema' } });
    }
  });

  it('a tailnet-range IPv4 anywhere: 400 secret_like_value with the path only', async () => {
    const value = secrets.tailnetIp();
    const snapshot = ai3();
    snapshot.ark.roots[0]!.ready!.items[0]!.reason = `copied from ${value}`;
    const error = await refusal(snapshot);
    const body = error.getResponse() as { details: Record<string, unknown> };
    expect(body.details).toEqual({ code: 'LAI-CLD-008', reason: 'secret_like_value', path: 'ark.roots[0].ready.items[0].reason' });
    expect(JSON.stringify(body)).not.toContain(value);
  });

  it('"lat_" + 43 characters is refused, even in a field the schema would reject anyway, without echoing it', async () => {
    const token = secrets.labToken();
    const snapshot = ai3() as unknown as Record<string, unknown>;
    snapshot.mode = token; // an invalid enum value: Zod would echo it as `received`; the scrubber runs first
    const error = await refusal(snapshot);
    const body = error.getResponse();
    expect(body).toMatchObject({ details: { reason: 'secret_like_value', path: 'mode' } });
    expect(JSON.stringify(body)).not.toContain(token);
  });

  it('a secret-looking value in an UNKNOWN key is refused as a secret, not echoed as a schema issue', async () => {
    const value = secrets.lanIp();
    const error = await refusal({ ...ai3(), extra: value });
    expect(error.getResponse()).toMatchObject({ details: { reason: 'secret_like_value', path: 'extra' } });
  });

  it('a hostile, deeply nested body is a 400, not a stack overflow', async () => {
    let deep: unknown = 'x';
    for (let i = 0; i < 50_000; i += 1) deep = [deep];
    const error = await refusal(deep);
    expect(error.getResponse()).toMatchObject({ details: { reason: 'schema', issues: [{ path: '$', code: 'too_large' }] } });
  });

  it('logs the path and rule of a refusal, never the value', async () => {
    const value = secrets.tailnetIp();
    const r = rig();
    const logger = (r.service as unknown as { logger: { warn: (m: string) => void } }).logger;
    const warn = jest.spyOn(logger, 'warn').mockImplementation(() => undefined);
    const snapshot = ai3();
    snapshot.profile = value;
    await r.service.ingest(NODE, USER, snapshot).catch(() => undefined);
    const logged = warn.mock.calls.map((call) => String(call[0])).join('\n');
    expect(logged).toContain('path=profile');
    expect(logged).toContain('rule=tailnet_ip');
    expect(logged).not.toContain(value);
  });
});
