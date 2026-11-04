import 'reflect-metadata';

import { ALERTS_EVALUATE_TYPE } from './alerts-evaluate.job';
import { AlertsEvaluateTask } from './alerts.cron';

function task(active: { id: string; status: string } | null = null) {
  const jobs = { enqueue: jest.fn(async () => ({ id: 'job-1' })) };
  const prisma = { job: { findFirst: jest.fn(async () => active) } };
  return { task: new AlertsEvaluateTask(jobs as never, prisma as never), jobs, prisma };
}

describe('AlertsEvaluateTask', () => {
  it('is scheduled every 5 minutes', () => {
    const keys = Reflect.getMetadataKeys(AlertsEvaluateTask.prototype.handleCron);
    const values = keys.map((key) => Reflect.getMetadata(key, AlertsEvaluateTask.prototype.handleCron) as unknown);
    expect(JSON.stringify(values)).toContain('*/5 * * * *');
  });

  it('only enqueues the evaluation job: a global housekeeping job with no organization and no subject', async () => {
    const { task: sut, jobs } = task();
    await sut.handleCron();
    expect(jobs.enqueue).toHaveBeenCalledTimes(1);
    expect(jobs.enqueue).toHaveBeenCalledWith(expect.objectContaining({ type: ALERTS_EVALUATE_TYPE, reason: 'backfill', orgId: null }));
  });

  it('skips the tick while an evaluation is still pending or running', async () => {
    const { task: sut, jobs, prisma } = task({ id: 'job-0', status: 'running' });
    await sut.handleCron();
    expect(prisma.job.findFirst).toHaveBeenCalledWith(expect.objectContaining({ where: { type: ALERTS_EVALUATE_TYPE, status: { in: ['pending', 'running'] } } }));
    expect(jobs.enqueue).not.toHaveBeenCalled();
  });

  it('never throws', async () => {
    const { task: sut, jobs } = task();
    jobs.enqueue.mockRejectedValueOnce(new Error('queue down'));
    await expect(sut.handleCron()).resolves.toBeUndefined();
  });
});
