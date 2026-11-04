import { JobHandlerRegistry } from '@marinoscar/platform-api/jobs';

import { VpsPublishHandler, VpsUnpublishHandler } from './vps-jobs.handlers';
import { JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH } from './vps.constants';
import { jobRow } from '../../../test/localai/support/vps-fakes';

const publishResult = { host: 'ai-n3.marin.cr', steps: [{ name: 'dns_upsert', status: 'done', detail: 'ok' }], certificateExpiresAt: null };

function build() {
  const registry = new JobHandlerRegistry();
  const broker = { kind: 'localai.vps.route53' };
  const state = { persist: jest.fn(async () => undefined) };
  const publish = new VpsPublishHandler(registry, broker as never, state as never);
  const unpublish = new VpsUnpublishHandler(registry, broker as never, state as never);
  publish.onModuleInit();
  unpublish.onModuleInit();
  return { registry, broker, state, publish, unpublish };
}

describe('the two VPS job handlers', () => {
  it('register under their permanent type strings', () => {
    const { registry } = build();
    expect(JOB_VPS_PUBLISH).toBe('localai.vps.publish_node');
    expect(JOB_VPS_UNPUBLISH).toBe('localai.vps.unpublish_node');
    expect(registry.get(JOB_VPS_PUBLISH)).toBeDefined();
    expect(registry.get(JOB_VPS_UNPUBLISH)).toBeDefined();
  });

  it('are node-eligible by derivation (result schema + persist) and carry a secret broker', () => {
    const { publish, unpublish, broker } = build();
    for (const handler of [publish, unpublish]) {
      expect(handler.nodeResultSchema).toBeDefined();
      expect(typeof handler.persistNodeResult).toBe('function');
      expect(handler.nodeSecretBroker).toBe(broker);
      expect(handler.profile.maxAttempts).toBe(1);
      expect(handler.profile.maxRuntimeMs).toBeGreaterThan(0);
    }
  });

  it('validate the node result with their strict schema (the trust boundary)', () => {
    const { publish, unpublish } = build();
    expect(publish.nodeResultSchema.safeParse(publishResult).success).toBe(true);
    expect(publish.nodeResultSchema.safeParse({ ...publishResult, extra: true }).success).toBe(false);
    expect(unpublish.nodeResultSchema.safeParse({ host: 'ai-n3.marin.cr', steps: [{ name: 'dns_delete', status: 'done', detail: 'x' }] }).success).toBe(true);
    expect(unpublish.nodeResultSchema.safeParse(publishResult).success).toBe(false);
  });

  it('persist through the state service with the right action', async () => {
    const { publish, unpublish, state } = build();
    await publish.persistNodeResult(jobRow(JOB_VPS_PUBLISH), publishResult);
    await unpublish.persistNodeResult(jobRow(JOB_VPS_UNPUBLISH), { host: 'ai-n3.marin.cr', steps: [] });
    expect(state.persist.mock.calls.map((call) => (call as unknown[])[1])).toEqual(['publish', 'unpublish']);
  });

  it('the API server’s own worker cannot run them and says why', async () => {
    const { publish, unpublish } = build();
    await expect(publish.process()).rejects.toThrow(/JOBS_WORKER_MODE=system/);
    await expect(unpublish.process()).rejects.toThrow(/stack-agent/);
  });
});
