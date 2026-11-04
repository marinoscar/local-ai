import { ConflictException, NotFoundException } from '@nestjs/common';

import { VpsService } from './vps.service';
import { fakeTailnet } from './vps-test-support';
import { JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH } from './vps.constants';

interface Options {
  node?: Record<string, unknown> | null;
  keyConfigured?: boolean;
  offered?: string[];
  agent?: { eligibleTypes: string[] } | null;
  latestJob?: Record<string, unknown> | null;
  suffix?: string;
  suffixValid?: boolean;
  workerMode?: string | undefined;
  checklist?: unknown;
}

function setup(options: Options = {}) {
  const tail = fakeTailnet();
  const node = options.node === undefined ? { nodeId: 'ai-n3', phase: 'enrolled', servePattern: 'B', ...tail, publishChecklist: options.checklist ?? {} } : options.node === null ? null : { publishChecklist: {}, ...options.node };
  const prisma = {
    localaiNode: { findUnique: jest.fn(async () => node) },
    job: { findFirst: jest.fn(async () => options.latestJob ?? null) },
    workerNode: { findFirst: jest.fn(async () => (options.agent === undefined ? { eligibleTypes: [JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH] } : options.agent)) },
  };
  const jobs = { enqueue: jest.fn(async () => ({ id: 'job-9' })) };
  const keys = { status: jest.fn(async () => ({ configured: options.keyConfigured ?? true })) };
  const settings = { read: jest.fn(async () => ({ stagingSuffix: options.suffix ?? '-staging', stagingSuffixValid: options.suffixValid ?? true, vpsPublicHostname: 'ai.marin.cr' })) };
  const offload = { offeredTypes: jest.fn(async () => options.offered ?? [JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH]) };
  const audit = { record: jest.fn(async () => undefined) };
  if ('workerMode' in options) {
    if (options.workerMode === undefined) delete process.env.JOBS_WORKER_MODE;
    else process.env.JOBS_WORKER_MODE = options.workerMode;
  } else process.env.JOBS_WORKER_MODE = 'system';
  return { service: new VpsService(prisma as never, jobs as never, keys as never, settings as never, offload as never, audit as never), jobs, audit, prisma };
}

const codes = (blockers: Array<{ code: string }>) => blockers.map((b) => b.code);
const saved = process.env.JOBS_WORKER_MODE;
afterAll(() => {
  if (saved === undefined) delete process.env.JOBS_WORKER_MODE;
  else process.env.JOBS_WORKER_MODE = saved;
});

describe('VpsService.status: what blocks a publish', () => {
  it('nothing blocks a fully prepared node', async () => {
    const status = await setup().service.status('ai-n3');
    expect(status.publish).toEqual({ available: true, blockers: [] });
    expect(status.unpublish.available).toBe(true);
    expect(status.host).toBe('ai-n3-staging.marin.cr');
    expect(status.run.state).toBe('idle');
  });

  it.each([
    ['a retired node', { node: { nodeId: 'ai-n3', phase: 'retired', servePattern: 'B', tailnetIp: 'x', tailnetMachine: 'a.b.c' } }, 'node_retired'],
    ['an invalid suffix', { suffixValid: false }, 'staging_suffix_invalid'],
    ['no Route53 key', { keyConfigured: false }, 'route53_not_configured'],
    ['secrets not offered', { offered: [] }, 'secrets_not_offered'],
    ['no agent', { agent: null }, 'agent_offline'],
    ['an agent that lists other types', { agent: { eligibleTypes: ['x'] } }, 'agent_offline'],
    ['the server worker in all mode', { workerMode: 'all' }, 'server_worker_claims_jobs'],
    ['the server worker with no mode (the default is all)', { workerMode: undefined }, 'server_worker_claims_jobs'],
    ['an active job', { latestJob: { id: 'j', type: JOB_VPS_PUBLISH, status: 'running', createdAt: new Date(), finishedAt: null, lastError: null } }, 'job_active'],
  ])('%s blocks both actions', async (_label, options, code) => {
    const status = await setup(options as Options).service.status('ai-n3');
    expect(codes(status.publish.blockers)).toContain(code);
    expect(codes(status.unpublish.blockers)).toContain(code);
    expect(status.publish.available).toBe(false);
  });

  it.each([
    ['no tailnet IP', { servePattern: 'B', tailnetIp: null, tailnetMachine: 'a.b.c' }],
    ['a short machine name on Pattern B', { servePattern: 'B', tailnetIp: 'x', tailnetMachine: 'dell-linux' }],
    ['no machine name', { servePattern: 'A', tailnetIp: 'x', tailnetMachine: null }],
  ])('%s blocks a publish only', async (_label, fields) => {
    const status = await setup({ node: { nodeId: 'ai-n3', phase: 'enrolled', ...fields } }).service.status('ai-n3');
    expect(codes(status.publish.blockers)).toContain('tailnet_incomplete');
    expect(status.unpublish.available).toBe(true);
  });

  it('a node with serve pattern none cannot be published, but can be unpublished', async () => {
    const status = await setup({ node: { nodeId: 'ai-n3', phase: 'enrolled', servePattern: 'none', tailnetIp: null, tailnetMachine: null } }).service.status('ai-n3');
    expect(codes(status.publish.blockers)).toContain('serve_pattern_none');
    expect(status.unpublish.available).toBe(true);
  });

  it('a short machine name is fine on Pattern A', async () => {
    const status = await setup({ node: { nodeId: 'ai-n3', phase: 'enrolled', servePattern: 'A', tailnetIp: 'x', tailnetMachine: 'dell-linux' } }).service.status('ai-n3');
    expect(status.publish.available).toBe(true);
  });

  it('blocker messages are plain sentences with no address or key', async () => {
    const status = await setup({ keyConfigured: false, offered: [], agent: null, workerMode: 'all', node: { nodeId: 'ai-n3', phase: 'enrolled', servePattern: 'B', tailnetIp: null, tailnetMachine: null } }).service.status('ai-n3');
    const text = JSON.stringify(status);
    expect(text).not.toMatch(/\d+\.\d+\.\d+\.\d+/);
    expect(status.publish.blockers.length).toBeGreaterThanOrEqual(5);
  });

  it('answers 404 for an unknown node', async () => {
    await expect(setup({ node: null }).service.status('ai-n3')).rejects.toBeInstanceOf(NotFoundException);
  });
});

describe('VpsService.status: the run and its journal', () => {
  const journal = { jobId: 'job-5', action: 'publish', host: 'ai-n3-staging.marin.cr', outcome: 'done', finishedAt: '2026-11-04T12:00:00.000Z', steps: [{ name: 'dns_upsert', status: 'done', detail: 'ok' }], certificateExpiresAt: '2027-02-01T00:00:00.000Z' };
  const job = (status: string, extra: Record<string, unknown> = {}) => ({ id: 'job-5', type: JOB_VPS_PUBLISH, status, createdAt: new Date('2026-11-04T11:59:00Z'), finishedAt: null, lastError: null, ...extra });

  it.each([['pending', 'queued'], ['running', 'running'], ['succeeded', 'succeeded'], ['failed', 'failed']])('job %s is state %s', async (jobStatus, state) => {
    const status = await setup({ latestJob: job(jobStatus), checklist: { vps: journal } }).service.status('ai-n3');
    expect(status.run.state).toBe(state);
    expect(status.run.action).toBe('publish');
    expect(status.run.jobId).toBe('job-5');
  });

  it('shows the steps of the journal only when it belongs to the latest job', async () => {
    const mine = await setup({ latestJob: job('succeeded'), checklist: { vps: journal } }).service.status('ai-n3');
    expect(mine.run.steps).toHaveLength(1);
    expect(mine.run.certificateExpiresAt).toBe('2027-02-01T00:00:00.000Z');
    const older = await setup({ latestJob: job('running'), checklist: { vps: { ...journal, jobId: 'job-4' } } }).service.status('ai-n3');
    expect(older.run.steps).toEqual([]);
  });

  it('a job that failed without a journal shows its scrubbed error', async () => {
    const status = await setup({ latestJob: job('failed', { lastError: `no secret ${fakeTailnet().tailnetIp}` }) }).service.status('ai-n3');
    expect(status.run.error).toContain('no secret');
    expect(status.run.error).not.toMatch(/\d+\.\d+\.\d+\.\d+/);
  });
});

describe('VpsService requests', () => {
  it('enqueues exactly { nodeId } for the right type, with the node as subject', async () => {
    const h = setup();
    expect(await h.service.requestPublish('ai-n3', 'user-1')).toEqual({ jobId: 'job-9' });
    expect(h.jobs.enqueue).toHaveBeenCalledWith({ type: JOB_VPS_PUBLISH, reason: 'upload', subjectType: 'localai_node', subjectId: 'ai-n3', payload: { nodeId: 'ai-n3' }, orgId: null });
    await h.service.requestUnpublish('ai-n3', 'user-1');
    expect(h.jobs.enqueue).toHaveBeenLastCalledWith(expect.objectContaining({ type: JOB_VPS_UNPUBLISH, payload: { nodeId: 'ai-n3' } }));
  });

  it('audits the request with the actor and no secret', async () => {
    const h = setup();
    await h.service.requestPublish('ai-n3', 'user-1');
    expect(h.audit.record).toHaveBeenCalledWith(expect.objectContaining({ action: 'localai.vps.publish_requested', actorUserId: 'user-1', targetId: 'ai-n3' }));
    expect(JSON.stringify(h.audit.record.mock.calls)).not.toMatch(/\d+\.\d+\.\d+\.\d+/);
  });

  it('refuses with 409 and the blocker code, and enqueues nothing', async () => {
    const h = setup({ keyConfigured: false });
    const error = (await h.service.requestPublish('ai-n3', 'user-1').catch((e: unknown) => e)) as ConflictException;
    expect(error).toBeInstanceOf(ConflictException);
    expect((error.getResponse() as { details: { reason: string } }).details.reason).toBe('route53_not_configured');
    expect(h.jobs.enqueue).not.toHaveBeenCalled();
  });

  it('does not queue an unpublish while a publish is running', async () => {
    const h = setup({ latestJob: { id: 'j', type: JOB_VPS_PUBLISH, status: 'pending', createdAt: new Date(), finishedAt: null, lastError: null } });
    await expect(h.service.requestUnpublish('ai-n3', null)).rejects.toBeInstanceOf(ConflictException);
  });
});
