// `CommandsService.request` and `.cancel` (CLOUD-09) over the in-memory command table: the permission matrix per
// type, every refusal and its order, dedup, the audit rows (exactly one per new or cancelled command) and the cancel
// race. The same paths over HTTP are `commands-api-routes.spec.ts`; real SQL is `commands-api.db.spec.ts`.
import { ConflictException, ForbiddenException, NotFoundException, UnprocessableEntityException } from '@nestjs/common';

import { COMMAND_POLICY, FEATURE_ONLY_TYPES } from '../../src/localai/commands/command-policy';
import { CommandsService } from '../../src/localai/commands/commands.service';
import { COMMAND_TYPES, type CommandType } from '../../src/localai/contracts/commands';
import { FakeBus, FakeClock, FakeCommandStore } from './support/fake-command-store';
import { readCommandExamples } from '../fixtures/localai/command-examples';

const NODE_ROW = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee1';
const MODEL = 'qwen3.6-35b-a3b-q4km';
const OPERATE = ['lab:read', 'lab:operate', 'gateway:use', 'localai_music:use', 'localai_workloads:write'];
/** The types the generic route queues (music.generate goes through `/api/localai/music/requests` only). */
const GENERIC_TYPES = COMMAND_TYPES.filter((type) => !FEATURE_ONLY_TYPES.has(type));
const ADMIN_ONLY_TYPES: CommandType[] = ['localai.model.delete', 'localai.mode.offline', 'localai.key.rotate', 'localai.agent.update'];

/** One valid params object per type: the shared wire fixture (the agent's fake cloud uses the same file). */
const EXAMPLES = readCommandExamples<Record<CommandType, { params: Record<string, unknown> }>>();
const PARAMS = Object.fromEntries(COMMAND_TYPES.map((type) => [type, EXAMPLES[type].params])) as Record<CommandType, Record<string, unknown>>;
const DELETE_PARAMS = PARAMS['localai.model.delete'];

const roles = {
  viewer: { userId: 'user-viewer', permissions: ['lab:read'] },
  contributor: { userId: 'user-contributor', permissions: OPERATE },
  admin: { userId: 'user-admin', permissions: ['lab:read', 'lab:operate', 'lab:admin', 'gateway:use', 'gateway:admin', 'localai_music:use', 'localai_music:delete', 'localai_workloads:write'] },
};

describe('CommandsService.request and cancel', () => {
  const clock = new FakeClock(new Date('2026-11-02T15:04:00.000Z'));
  let store: FakeCommandStore;
  let bus: FakeBus;
  let audit: { record: jest.Mock };
  let node: { id: string; nodeId: string; phase: string; mode: string | null } | null;
  let model: { present: boolean } | null;
  let service: CommandsService;

  beforeEach(() => {
    store = new FakeCommandStore(clock);
    store.knownNodes.set('ai-n2', NODE_ROW);
    bus = new FakeBus();
    audit = { record: jest.fn().mockResolvedValue(undefined) };
    node = { id: NODE_ROW, nodeId: 'ai-n2', phase: 'enrolled', mode: 'online' };
    model = { present: true };
    const prisma = {
      localaiCommand: store.localaiCommand,
      localaiNode: { findUnique: jest.fn(async () => node) },
      localaiNodeModel: { findUnique: jest.fn(async () => model) },
    };
    service = new CommandsService(prisma as never, bus as never, clock, audit);
  });

  describe('the permission matrix (spec 3.7)', () => {
    it.each(GENERIC_TYPES)('%s: viewer 403, contributor %s, admin 2xx', async (type) => {
      const short = type.replace('localai.', '');
      await expect(service.request('ai-n2', short, PARAMS[type], roles.viewer, 'ui')).rejects.toBeInstanceOf(ForbiddenException);
      expect(store.rows).toHaveLength(0);

      const contributor = service.request('ai-n2', short, PARAMS[type], roles.contributor, 'ui');
      if (ADMIN_ONLY_TYPES.includes(type)) {
        await expect(contributor).rejects.toBeInstanceOf(ForbiddenException);
        expect(store.rows).toHaveLength(0);
      } else {
        await expect(contributor).resolves.toMatchObject({ created: true, command: { type, status: 'queued', nodeId: 'ai-n2' } });
        store.rows.length = 0;
      }

      await expect(service.request('ai-n2', short, PARAMS[type], roles.admin, 'ui')).resolves.toMatchObject({ created: true, command: { type } });
    });

    it('the table decides: the permission named in the 403 is the policy table\'s', async () => {
      for (const type of GENERIC_TYPES) {
        const error = (await service.request('ai-n2', type, PARAMS[type], roles.viewer, 'ui').catch((e) => e)) as ForbiddenException;
        expect((error.getResponse() as { details: { permission: string } }).details.permission).toBe(COMMAND_POLICY[type].permission);
      }
    });

    it('workload.activate needs localai_workloads:write exactly: lab:operate alone is refused', async () => {
      const error = await service.request('ai-n2', 'workload.activate', { name: 'music' }, { userId: 'u', permissions: ['lab:read', 'lab:operate'] }, 'ui').catch((e) => e);
      expect(error).toBeInstanceOf(ForbiddenException);
      expect(error.getResponse().details).toMatchObject({ permission: 'localai_workloads:write' });
      await expect(service.request('ai-n2', 'workload.activate', { name: 'music' }, { userId: 'u', permissions: ['localai_workloads:write'] }, 'ui')).resolves.toMatchObject({ created: true });
    });

    it('music.generate is refused on the generic route (400 feature_only_command) for everyone, and nothing is queued', async () => {
      for (const who of [roles.viewer, roles.contributor, roles.admin]) {
        const error = await service.request('ai-n2', 'music.generate', PARAMS['localai.music.generate'], who, 'ui').catch((e) => e);
        expect(error.getStatus()).toBe(400);
        expect(error.getResponse().details).toMatchObject({ reason: 'feature_only_command', route: '/api/localai/music/requests' });
      }
      expect(store.rows).toHaveLength(0);
      expect(audit.record).not.toHaveBeenCalled();
    });

    it('lab:admin alone does not stand in for lab:operate on an operate type', async () => {
      await expect(service.request('ai-n2', 'model.load', PARAMS['localai.model.load'], { userId: 'u', permissions: ['lab:admin'] }, 'ui')).rejects.toBeInstanceOf(ForbiddenException);
    });
  });

  describe('type and params', () => {
    it('accepts the unprefixed and the prefixed type and answers with the prefixed one', async () => {
      const bare = await service.request('ai-n2', 'model.load', { model_id: MODEL, unload_others: true }, roles.contributor, 'ui');
      expect(bare.command).toMatchObject({ type: 'localai.model.load', params: { model_id: MODEL, wait: true, unload_others: true, set_default: false } });
      const prefixed = await service.request('ai-n2', 'localai.model.unload', { model_id: MODEL }, roles.contributor, 'ui');
      expect(prefixed.command.type).toBe('localai.model.unload');
    });

    it('an unknown type is a 400 (not a 403 or 404) and writes nothing', async () => {
      const error = await service.request('ai-n2', 'shell.exec', {}, roles.admin, 'ui').catch((e) => e);
      expect(error.getStatus()).toBe(400);
      expect(error.getResponse().details).toMatchObject({ reason: 'unknown_command_type' });
      expect(store.rows).toHaveLength(0);
      expect(audit.record).not.toHaveBeenCalled();
    });

    it('params that fail the strict schema are a 400 with the Zod issues, and unknown keys are refused', async () => {
      const bad = await service.request('ai-n2', 'model.load', { model_id: 'Not A Model' }, roles.contributor, 'ui').catch((e) => e);
      expect(bad.getStatus()).toBe(400);
      expect(bad.getResponse().details).toMatchObject({ reason: 'invalid_params', issues: [expect.objectContaining({ path: ['model_id'] })] });
      const extra = await service.request('ai-n2', 'status.refresh', { rm: '-rf' }, roles.contributor, 'ui').catch((e) => e);
      expect(extra.getStatus()).toBe(400);
      expect(store.rows).toHaveLength(0);
    });

    it('model.delete needs confirm equal to model_id', async () => {
      const error = await service.request('ai-n2', 'model.delete', { ...DELETE_PARAMS, confirm: 'nope' }, roles.admin, 'ui').catch((e) => e);
      expect(error.getStatus()).toBe(400);
      expect(store.rows).toHaveLength(0);
    });
  });

  describe('node refusals', () => {
    it('an unknown node is 404', async () => {
      node = null;
      await expect(service.request('ai-n9', 'status.refresh', {}, roles.contributor, 'ui')).rejects.toBeInstanceOf(NotFoundException);
    });

    it.each(['pending', 'retired'])('a %s node is 409 LAI-CLD-035 and nothing is queued or audited', async (phase) => {
      node = { id: NODE_ROW, nodeId: 'ai-n2', phase, mode: null };
      const error = await service.request('ai-n2', 'status.refresh', {}, roles.contributor, 'ui').catch((e) => e);
      expect(error).toBeInstanceOf(ConflictException);
      expect(error.getResponse().details).toMatchObject({ code: 'LAI-CLD-035' });
      expect(store.rows).toHaveLength(0);
      expect(audit.record).not.toHaveBeenCalled();
    });

    it.each(['offline', 'survival'])('a node in %s mode is 422 LAI-CLD-034 for every type', async (mode) => {
      node = { id: NODE_ROW, nodeId: 'ai-n2', phase: 'enrolled', mode };
      for (const type of GENERIC_TYPES) {
        const error = await service.request('ai-n2', type, PARAMS[type], roles.admin, 'ui').catch((e) => e);
        expect(error).toBeInstanceOf(UnprocessableEntityException);
        expect(error.getResponse().details).toMatchObject({ code: 'LAI-CLD-034' });
      }
      expect(store.rows).toHaveLength(0);
    });

    it('a viewer is refused with 403 before anything about the node is revealed', async () => {
      node = null;
      await expect(service.request('ai-n9', 'status.refresh', {}, roles.viewer, 'ui')).rejects.toBeInstanceOf(ForbiddenException);
    });
  });

  describe('the model must be on the node', () => {
    const MODEL_TYPES: CommandType[] = ['localai.model.load', 'localai.model.unload', 'localai.model.set_default', 'localai.model.delete'];

    it.each(MODEL_TYPES)('%s of a model that is not in the snapshot is 422 "download it first"', async (type) => {
      model = null;
      const error = await service.request('ai-n2', type, PARAMS[type], roles.admin, 'ui').catch((e) => e);
      expect(error).toBeInstanceOf(UnprocessableEntityException);
      expect(error.message).toContain('Download it first');
      expect(store.rows).toHaveLength(0);
    });

    it('a model listed but not downloaded (present: false) is refused the same way', async () => {
      model = { present: false };
      await expect(service.request('ai-n2', 'model.load', { model_id: MODEL }, roles.contributor, 'ui')).rejects.toBeInstanceOf(UnprocessableEntityException);
    });

    it('model.download of an absent model is 201: it is how the model gets there', async () => {
      model = null;
      await expect(service.request('ai-n2', 'model.download', { model_id: MODEL }, roles.contributor, 'ui')).resolves.toMatchObject({ created: true });
    });

    it('a type with no model never reads the snapshot', async () => {
      model = null;
      await expect(service.request('ai-n2', 'service.restart', { service: 'llama' }, roles.contributor, 'ui')).resolves.toMatchObject({ created: true });
    });
  });

  describe('enqueue, dedup and audit', () => {
    it('stores the source and the requester, and audits localai.command.requested exactly once', async () => {
      const { command, created } = await service.request('ai-n2', 'model.load', { model_id: MODEL }, roles.contributor, 'api');
      expect(created).toBe(true);
      expect(store.get(command.id)).toMatchObject({ source: 'api', requestedById: 'user-contributor', dedupKey: `model.load:${MODEL}` });
      expect(audit.record).toHaveBeenCalledTimes(1);
      expect(audit.record).toHaveBeenCalledWith({
        action: 'localai.command.requested',
        actorUserId: 'user-contributor',
        targetType: 'localai_command',
        targetId: command.id,
        meta: { nodeId: 'ai-n2', type: 'localai.model.load', params: JSON.stringify({ model_id: MODEL, wait: true, unload_others: true, set_default: false }) },
      });
      expect(bus.on('localai.command.enqueued')).toEqual([{ nodeId: NODE_ROW }]);
    });

    it('a second identical request returns the active row (created: false) and writes and audits nothing more', async () => {
      const first = await service.request('ai-n2', 'model.load', { model_id: MODEL }, roles.contributor, 'ui');
      const second = await service.request('ai-n2', 'model.load', { model_id: MODEL, unload_others: true }, roles.admin, 'ui');
      expect(second.created).toBe(false);
      expect(second.command.id).toBe(first.command.id);
      expect(store.rows).toHaveLength(1);
      expect(audit.record).toHaveBeenCalledTimes(1);
    });

    it('a failing audit sink never fails the request', async () => {
      audit.record.mockRejectedValue(new Error('audit table down'));
      await expect(service.request('ai-n2', 'status.refresh', {}, roles.contributor, 'ui')).resolves.toMatchObject({ created: true });
      expect(store.rows).toHaveLength(1);
    });

    it('audit meta holds scalars only', async () => {
      await service.request('ai-n2', 'logs.tail', { unit: 'agent' }, roles.contributor, 'ui');
      const meta = audit.record.mock.calls[0][0].meta as Record<string, unknown>;
      for (const value of Object.values(meta)) expect(['string', 'number', 'boolean']).toContain(typeof value);
    });
  });

  describe('cancel', () => {
    async function queued(type: string = 'model.load', params: Record<string, unknown> = { model_id: MODEL }) {
      const { command } = await service.request('ai-n2', type, params, roles.admin, 'ui');
      audit.record.mockClear();
      return command;
    }

    it('cancels a queued command, audits localai.command.cancelled once and frees the dedup key', async () => {
      const command = await queued();
      const cancelled = await service.cancel(command.id, roles.contributor);
      expect(cancelled).toMatchObject({ id: command.id, status: 'cancelled', nodeId: 'ai-n2' });
      expect(cancelled.finishedAt).toBe(clock.now().toISOString());
      expect(store.get(command.id).status).toBe('cancelled');
      expect(audit.record).toHaveBeenCalledTimes(1);
      expect(audit.record).toHaveBeenCalledWith(
        expect.objectContaining({ action: 'localai.command.cancelled', actorUserId: 'user-contributor', targetType: 'localai_command', targetId: command.id }),
      );
      const again = await service.request('ai-n2', 'model.load', { model_id: MODEL }, roles.contributor, 'ui');
      expect(again.created).toBe(true);
      expect(again.command.id).not.toBe(command.id);
    });

    it('a running command is 409 and stays running; nothing is audited', async () => {
      const command = await queued();
      store.get(command.id).status = 'running';
      const error = await service.cancel(command.id, roles.admin).catch((e) => e);
      expect(error).toBeInstanceOf(ConflictException);
      expect(error.getResponse().details).toMatchObject({ reason: 'not_cancellable', status: 'running' });
      expect(store.get(command.id).status).toBe('running');
      expect(audit.record).not.toHaveBeenCalled();
    });

    it.each(['succeeded', 'failed', 'expired', 'cancelled'] as const)('a %s command is 409', async (status) => {
      const command = await queued();
      store.get(command.id).status = status;
      await expect(service.cancel(command.id, roles.admin)).rejects.toBeInstanceOf(ConflictException);
    });

    it('a viewer is 403; a contributor is 403 on an admin type and an admin may cancel it', async () => {
      const operate = await queued();
      await expect(service.cancel(operate.id, roles.viewer)).rejects.toBeInstanceOf(ForbiddenException);
      const destructive = await queued('model.delete', DELETE_PARAMS);
      await expect(service.cancel(destructive.id, roles.contributor)).rejects.toBeInstanceOf(ForbiddenException);
      expect(store.get(destructive.id).status).toBe('queued');
      await expect(service.cancel(destructive.id, roles.admin)).resolves.toMatchObject({ status: 'cancelled' });
    });

    it('an unknown id is 404', async () => {
      await expect(service.cancel('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', roles.admin)).rejects.toBeInstanceOf(NotFoundException);
    });

    it('a node that claims between the read and the write wins: the conditional write changes 0 rows and the answer is 409', async () => {
      const command = await queued();
      const original = store.localaiCommand.updateMany;
      store.localaiCommand.updateMany = async (args) => {
        store.get(command.id).status = 'running'; // the claim lands first
        return original(args);
      };
      const error = await service.cancel(command.id, roles.admin).catch((e) => e);
      expect(error).toBeInstanceOf(ConflictException);
      expect(error.getResponse().details).toMatchObject({ status: 'running' });
      expect(audit.record).not.toHaveBeenCalled();
    });
  });
});
