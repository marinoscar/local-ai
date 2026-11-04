import 'reflect-metadata';

import { COMMANDS_SWEEP_TYPE } from './sweeper.job';
import { CommandsSweepTask } from './sweeper.task';

function task(active: { id: string; status: string } | null = null) {
  const jobs = { enqueue: jest.fn(async () => ({ id: 'job-1' })) };
  const prisma = { job: { findFirst: jest.fn(async () => active) } };
  return { task: new CommandsSweepTask(jobs as never, prisma as never), jobs, prisma };
}

describe('CommandsSweepTask', () => {
  it('is scheduled every 30 seconds', () => {
    // @nestjs/schedule stores the cron expression as metadata on the method.
    const keys = Reflect.getMetadataKeys(CommandsSweepTask.prototype.handleCron);
    const values = keys.map((key) => Reflect.getMetadata(key, CommandsSweepTask.prototype.handleCron) as unknown);
    expect(JSON.stringify(values)).toContain('*/30 * * * * *');
  });

  it('only enqueues the sweep job: a global housekeeping job with no organization', async () => {
    const { task: sut, jobs } = task();
    await sut.handleCron();
    expect(jobs.enqueue).toHaveBeenCalledTimes(1);
    expect(jobs.enqueue).toHaveBeenCalledWith(expect.objectContaining({ type: COMMANDS_SWEEP_TYPE, reason: 'backfill', orgId: null }));
    expect(jobs.enqueue.mock.calls[0]).not.toContainEqual(expect.objectContaining({ subjectId: expect.anything() }));
  });

  it('skips the tick while a sweep is still pending or running', async () => {
    const { task: sut, jobs, prisma } = task({ id: 'job-0', status: 'running' });
    await sut.handleCron();
    expect(prisma.job.findFirst).toHaveBeenCalledWith(expect.objectContaining({ where: { type: COMMANDS_SWEEP_TYPE, status: { in: ['pending', 'running'] } } }));
    expect(jobs.enqueue).not.toHaveBeenCalled();
  });

  it('never throws: a failed enqueue is logged and the next tick tries again', async () => {
    const { task: sut, jobs } = task();
    jobs.enqueue.mockRejectedValueOnce(new Error('queue down'));
    await expect(sut.handleCron()).resolves.toBeUndefined();
  });
});
