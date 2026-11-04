import 'reflect-metadata';

import { ALERTS_EVALUATE_TYPE, AlertsEvaluateHandler } from './alerts-evaluate.job';
import { JobsAlertReevaluator } from './alert-reevaluator';

const summary = { nodes: 1, raised: 0, escalated: 0, cleared: 0, notified: 0 };

function handler() {
  const registry = { register: jest.fn() };
  const alerts = { evaluateAll: jest.fn(async () => summary), evaluateNode: jest.fn(async () => summary) };
  return { handler: new AlertsEvaluateHandler(registry as never, alerts as never), registry, alerts };
}

describe('AlertsEvaluateHandler', () => {
  it('registers itself under the permanent type, server-only with a one-attempt profile', () => {
    const { handler: sut, registry } = handler();
    sut.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(sut);
    expect(sut.type).toBe('localai.alerts.evaluate');
    expect(sut.profile).toEqual({ maxRuntimeMs: 120_000, maxAttempts: 1 });
    expect(sut).not.toHaveProperty('nodeResultSchema');
    expect(sut).not.toHaveProperty('persistNodeResult');
  });

  it('with no payload evaluates every node', async () => {
    const { handler: sut, alerts } = handler();
    await sut.process({ payload: null } as never);
    expect(alerts.evaluateAll).toHaveBeenCalledTimes(1);
    expect(alerts.evaluateNode).not.toHaveBeenCalled();
  });

  it('with { nodeId } evaluates that node only', async () => {
    const { handler: sut, alerts } = handler();
    await sut.process({ payload: { nodeId: 'ai-n2' } } as never);
    expect(alerts.evaluateNode).toHaveBeenCalledWith('ai-n2');
    expect(alerts.evaluateAll).not.toHaveBeenCalled();
  });

  it('refuses a payload it does not know', async () => {
    const { handler: sut } = handler();
    await expect(sut.process({ payload: { nodeId: 'ai-n2', extra: true } } as never)).rejects.toThrow();
  });
});

describe('JobsAlertReevaluator', () => {
  it('queues a one-node evaluation instead of evaluating inline', async () => {
    const jobs = { enqueue: jest.fn(async () => ({ id: 'job-1' })) };
    await new JobsAlertReevaluator(jobs as never).reevaluateNode('ai-n3');
    expect(jobs.enqueue).toHaveBeenCalledWith(
      expect.objectContaining({ type: ALERTS_EVALUATE_TYPE, subjectType: 'localai_node', subjectId: 'ai-n3', payload: { nodeId: 'ai-n3' }, orgId: null, skipDedup: true }),
    );
  });
});
