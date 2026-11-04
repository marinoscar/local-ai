import { buildJournal, checklistUpdates, failedStep, readJournal, reservedEntries } from './vps-journal';
import type { StoredJournal } from './vps-result.schema';

const now = new Date('2026-11-04T12:00:00Z');
const step = (name: string, status: 'done' | 'skipped' | 'failed', detail = 'd') => ({ name, status, detail });
const publishSteps = (statuses: Partial<Record<string, 'done' | 'skipped' | 'failed'>> = {}) =>
  ['dns_upsert', 'dns_wait', 'bootstrap_vhost', 'certificate', 'final_vhost', 'validate'].map((name) => step(name, statuses[name] ?? 'done'));
const journal = (action: 'publish' | 'unpublish', steps: ReturnType<typeof publishSteps>): StoredJournal =>
  buildJournal({ jobId: 'job-1', action, host: 'ai-n3.marin.cr', steps, certificateExpiresAt: null, now });

describe('checklistUpdates (publish)', () => {
  it('marks steps 2 to 5 done after a full run, skipped counts as done', () => {
    const updates = checklistUpdates(journal('publish', publishSteps({ dns_upsert: 'skipped', certificate: 'skipped' })), now);
    expect(Object.keys(updates).sort()).toEqual(['bootstrap_vhost', 'certificate', 'dns', 'https_vhost']);
    for (const entry of Object.values(updates)) expect(entry).toMatchObject({ state: 'done', checkedAt: now.toISOString() });
  });

  it('marks the step of a failed journal step failed with its detail, and leaves unreached steps alone', () => {
    const steps = publishSteps().slice(0, 4);
    steps[3] = step('certificate', 'failed', 'certbot did not issue the certificate.');
    const updates = checklistUpdates(journal('publish', steps), now);
    expect(updates.dns?.state).toBe('done');
    expect(updates.bootstrap_vhost?.state).toBe('done');
    expect(updates.certificate).toMatchObject({ state: 'failed', detail: 'certbot did not issue the certificate.' });
    expect(updates.https_vhost).toBeUndefined();
  });

  it('a run that stops after final_vhost without a validate does not complete https_vhost', () => {
    const updates = checklistUpdates(journal('publish', publishSteps().slice(0, 5)), now);
    expect(updates.https_vhost).toBeUndefined();
  });

  it('a failed validate fails https_vhost', () => {
    const updates = checklistUpdates(journal('publish', publishSteps({ validate: 'failed' })), now);
    expect(updates.https_vhost?.state).toBe('failed');
    expect(updates.dns?.state).toBe('done');
  });

  it('scrubs a detail that carries an address', () => {
    const steps = publishSteps({ dns_wait: 'failed' });
    steps[1] = step('dns_wait', 'failed', `resolver ${[203, 0, 113, 10].join('.')} said no`);
    expect(checklistUpdates(journal('publish', steps), now).dns?.detail).not.toContain('203.0.113');
  });
});

describe('checklistUpdates (unpublish)', () => {
  const unpublishSteps = (failed = false) => [step('vhost_remove', failed ? 'failed' : 'done'), step('dns_delete', 'done'), step('certificate_kept', 'skipped')];

  it('puts steps 2 to 5 back to todo', () => {
    const updates = checklistUpdates(journal('unpublish', unpublishSteps()), now);
    expect(Object.keys(updates).sort()).toEqual(['bootstrap_vhost', 'certificate', 'dns', 'https_vhost']);
    for (const entry of Object.values(updates)) expect(entry).toMatchObject({ state: 'todo' });
  });

  it('a failed unpublish changes nothing', () => {
    expect(checklistUpdates(journal('unpublish', unpublishSteps(true)), now)).toEqual({});
  });
});

describe('the kept journal', () => {
  it('round-trips through the checklist JSON and survives a rewrite of the operator steps', () => {
    const kept = journal('publish', publishSteps());
    const stored = { dns: { state: 'done', checkedAt: null, detail: null }, vps: kept };
    expect(readJournal(stored)).toEqual(kept);
    expect(reservedEntries(stored)).toEqual({ vps: kept });
    expect(reservedEntries({})).toEqual({});
    expect(reservedEntries(null)).toEqual({});
  });

  it('is read defensively', () => {
    expect(readJournal(null)).toBeNull();
    expect(readJournal({ vps: 'x' })).toBeNull();
    expect(readJournal({ vps: { jobId: 'j', host: 'h', action: 'bad', outcome: 'done', steps: [] } })).toBeNull();
    const parsed = readJournal({ vps: { jobId: 'j', host: 'h', action: 'publish', outcome: 'done', steps: [{ name: 'a', status: 'nope', detail: 'x' }, { name: 'b', status: 'done', detail: 'ok' }] } });
    expect(parsed?.steps).toEqual([{ name: 'b', status: 'done', detail: 'ok' }]);
  });

  it('outcome and failedStep follow the steps', () => {
    expect(journal('publish', publishSteps()).outcome).toBe('done');
    const failed = journal('publish', publishSteps({ certificate: 'failed' }));
    expect(failed.outcome).toBe('failed');
    expect(failedStep(failed)?.name).toBe('certificate');
    expect(failedStep(journal('publish', publishSteps()))).toBeNull();
  });
});
