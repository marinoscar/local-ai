import { VpsStateService } from './vps-state.service';
import { fakeKey, fakeTailnet, jobRow } from './vps-test-support';
import { JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH } from './vps.constants';

const publishSteps = (over: Record<string, string> = {}) =>
  ['dns_upsert', 'dns_wait', 'bootstrap_vhost', 'certificate', 'final_vhost', 'validate'].map((name) => ({ name, status: over[name] ?? 'done', detail: `${name} ok` }));

function setup(options: { stored?: unknown; phase?: string; suffix?: string } = {}) {
  let stored: unknown = options.stored ?? { tailnet: { state: 'done', checkedAt: null, detail: null } };
  const tx = {
    $queryRaw: jest.fn(async () => []),
    localaiNode: {
      findUniqueOrThrow: jest.fn(async () => ({ publishChecklist: stored, phase: options.phase ?? 'enrolled' })),
      update: jest.fn(async ({ data }: { data: { publishChecklist: unknown } }) => {
        stored = data.publishChecklist;
      }),
    },
  };
  const prisma = { localaiNode: { findUnique: jest.fn(async () => ({ id: 'row-1' })) }, $transaction: jest.fn(async (fn: (t: typeof tx) => unknown) => fn(tx)) };
  const settings = { read: jest.fn(async () => ({ stagingSuffix: options.suffix ?? '', stagingSuffixValid: true, vpsPublicHostname: 'ai.marin.cr' })) };
  const audit = { record: jest.fn(async () => undefined) };
  return { service: new VpsStateService(prisma as never, settings as never, audit as never), get stored() { return stored as Record<string, any>; }, audit, tx };
}

const result = (host = 'ai-n3.marin.cr', over: Record<string, string> = {}) => ({ host, steps: publishSteps(over) as never, certificateExpiresAt: '2027-02-01T00:00:00.000Z' });

describe('VpsStateService.persist', () => {
  it('stores the journal beside the operator steps and marks steps 2 to 5 done', async () => {
    const h = setup();
    await h.service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', result());
    expect(h.stored.tailnet.state).toBe('done'); // untouched
    for (const step of ['dns', 'bootstrap_vhost', 'certificate', 'https_vhost']) expect(h.stored[step].state).toBe('done');
    expect(h.stored.vps).toMatchObject({ jobId: 'job-1', action: 'publish', host: 'ai-n3.marin.cr', outcome: 'done', certificateExpiresAt: '2027-02-01T00:00:00.000Z' });
    expect(h.tx.$queryRaw).toHaveBeenCalled(); // row lock
    expect(h.audit.record).toHaveBeenCalledWith(expect.objectContaining({ action: 'localai.vps.published', actorUserId: null, targetId: 'ai-n3' }));
  });

  it('accepts the staging host only under the configured suffix', async () => {
    const h = setup({ suffix: '-staging' });
    await h.service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', result('ai-n3-staging.marin.cr'));
    expect(h.stored.vps.host).toBe('ai-n3-staging.marin.cr');
    const wrong = setup({ suffix: '' });
    await expect(wrong.service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', result('ai-n3-staging.marin.cr'))).rejects.toThrow(/not the host of this node/);
    expect(wrong.tx.localaiNode.update).not.toHaveBeenCalled();
    await expect(setup().service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', result('ai-n4.marin.cr'))).rejects.toThrow(/not the host of this node/);
  });

  it('stores a failed journal, marks the failed step, then fails the job naming the step only', async () => {
    const h = setup();
    const error = (await h.service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', result('ai-n3.marin.cr', { certificate: 'failed' })).catch((e: unknown) => e)) as Error;
    expect(error.message).toBe('The VPS publish did not finish: step certificate failed.');
    expect(h.stored.certificate.state).toBe('failed');
    expect(h.stored.vps.outcome).toBe('failed');
    expect(h.audit.record).toHaveBeenCalledWith(expect.objectContaining({ action: 'localai.vps.publish_failed', meta: expect.objectContaining({ failedStep: 'certificate' }) }));
  });

  it('unpublish puts steps 2 to 5 back to todo', async () => {
    const h = setup({ stored: { dns: { state: 'done', checkedAt: null, detail: null }, https_vhost: { state: 'done', checkedAt: null, detail: null } } });
    await h.service.persist(jobRow(JOB_VPS_UNPUBLISH), 'unpublish', { host: 'ai-n3.marin.cr', steps: [{ name: 'vhost_remove', status: 'done', detail: 'ok' }] as never });
    for (const step of ['dns', 'bootstrap_vhost', 'certificate', 'https_vhost']) expect(h.stored[step].state).toBe('todo');
    expect(h.audit.record).toHaveBeenCalledWith(expect.objectContaining({ action: 'localai.vps.unpublished' }));
  });

  it('a retired node keeps its journal but its checklist is not touched', async () => {
    const h = setup({ phase: 'retired', stored: {} });
    await h.service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', result());
    expect(h.stored.dns).toBeUndefined();
    expect(h.stored.vps.outcome).toBe('done');
  });

  it('nothing stored, audited or thrown carries a tailnet value, a key or an address, even from a hostile node', async () => {
    const h = setup();
    const key = fakeKey();
    const tail = fakeTailnet();
    const hostile = { host: 'ai-n3.marin.cr', steps: publishSteps().map((s) => ({ ...s, detail: `${key.accessKeyId} ${key.secretAccessKey} ${tail.tailnetIp} ${tail.tailnetMachine}` })) as never, certificateExpiresAt: null };
    await h.service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', hostile);
    const dump = JSON.stringify(h.stored) + JSON.stringify(h.audit.record.mock.calls);
    for (const secret of [key.accessKeyId, key.secretAccessKey, tail.tailnetIp, tail.tailnetMachine, tail.tailnetMachine.split('.')[1]!]) expect(dump).not.toContain(secret);
  });

  it('a failed audit write does not lose the journal', async () => {
    const h = setup();
    h.audit.record.mockRejectedValue(new Error('db down'));
    await expect(h.service.persist(jobRow(JOB_VPS_PUBLISH), 'publish', result())).resolves.toBeUndefined();
    expect(h.stored.vps.outcome).toBe('done');
  });
});
