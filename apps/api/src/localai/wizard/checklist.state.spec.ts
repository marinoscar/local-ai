import { entryFromCheck, entryFromManual, isComplete, parseStoredChecklist, refusalFor, STEP_KIND, viewSteps, type StoredChecklist } from './checklist.state';
import { CHECKLIST_STEPS } from './wizard.schemas';

const NOW = new Date('2026-11-04T10:00:00.000Z');
const pending = { phase: 'pending' as const, keyReportedAt: null };
const everything: StoredChecklist = Object.fromEntries(
  (['tailnet', 'dns', 'bootstrap_vhost', 'certificate', 'https_vhost', 'gateway_test'] as const).map((step) => [step, { state: 'done', checkedAt: NOW.toISOString(), detail: 'ok' }]),
);

describe('viewSteps', () => {
  it('lists the eight steps in order, all to do for a new node', () => {
    const steps = viewSteps({}, pending);
    expect(steps.map((s) => s.step)).toEqual([...CHECKLIST_STEPS]);
    expect(steps.map((s) => s.order)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(steps.every((s) => s.state === 'todo')).toBe(true);
    expect(isComplete(steps)).toBe(false);
  });

  it('turns "enrolled" green when the node is enrolled, and "key" when its key arrived, by themselves', () => {
    const keyAt = new Date('2026-11-04T09:59:00.000Z');
    const steps = viewSteps({}, { phase: 'enrolled', keyReportedAt: keyAt });
    expect(steps.find((s) => s.step === 'enrolled')).toMatchObject({ state: 'done', kind: 'auto' });
    expect(steps.find((s) => s.step === 'key')).toMatchObject({ state: 'done', kind: 'auto', checkedAt: keyAt.toISOString() });
    const enrolledOnly = viewSteps({}, { phase: 'enrolled', keyReportedAt: null });
    expect(enrolledOnly.find((s) => s.step === 'enrolled')!.state).toBe('done');
    expect(enrolledOnly.find((s) => s.step === 'key')!.state).toBe('todo');
  });

  it('ignores a stored value for an automatic step: the node row decides', () => {
    const lying: StoredChecklist = { enrolled: { state: 'done', checkedAt: null, detail: null }, key: { state: 'done', checkedAt: null, detail: null } };
    const steps = viewSteps(lying, pending);
    expect(steps.find((s) => s.step === 'enrolled')!.state).toBe('todo');
    expect(steps.find((s) => s.step === 'key')!.state).toBe('todo');
  });

  it('is complete only when all eight are done', () => {
    expect(isComplete(viewSteps(everything, { phase: 'enrolled', keyReportedAt: NOW }))).toBe(true);
    expect(isComplete(viewSteps(everything, { phase: 'enrolled', keyReportedAt: null }))).toBe(false);
    expect(isComplete(viewSteps({ ...everything, dns: { state: 'failed', checkedAt: null, detail: 'x' } }, { phase: 'enrolled', keyReportedAt: NOW }))).toBe(false);
  });
});

describe('parseStoredChecklist', () => {
  it('reads the documented shape', () => {
    expect(parseStoredChecklist({ dns: { state: 'failed', checkedAt: '2026-11-04T10:00:00.000Z', detail: 'No A record' } })).toEqual({ dns: { state: 'failed', checkedAt: '2026-11-04T10:00:00.000Z', detail: 'No A record' } });
  });
  it('drops unknown steps and malformed entries instead of throwing', () => {
    expect(parseStoredChecklist({ nope: { state: 'done' }, dns: { state: 'maybe' }, certificate: 'x', tailnet: { state: 'done' } })).toEqual({ tailnet: { state: 'done', checkedAt: null, detail: null } });
    for (const junk of [null, undefined, 'x', 3, [], [{ state: 'done' }]]) expect(parseStoredChecklist(junk)).toEqual({});
  });
});

describe('refusalFor: which action each kind of step takes', () => {
  const cases: Array<[string, 'mark_done' | 'mark_todo' | 'check', string | null]> = [
    ['tailnet', 'mark_done', null],
    ['tailnet', 'mark_todo', null],
    ['tailnet', 'check', 'not_a_check_step'],
    ['bootstrap_vhost', 'mark_done', null],
    ['bootstrap_vhost', 'check', 'not_a_check_step'],
    ['dns', 'check', null],
    ['dns', 'mark_todo', null],
    ['dns', 'mark_done', 'not_a_manual_step'],
    ['certificate', 'mark_done', 'not_a_manual_step'],
    ['https_vhost', 'check', null],
    ['gateway_test', 'check', null],
    ['gateway_test', 'mark_done', 'not_a_manual_step'],
    ['enrolled', 'mark_done', 'automatic_step'],
    ['enrolled', 'check', 'automatic_step'],
    ['key', 'mark_todo', 'automatic_step'],
  ];
  it.each(cases)('%s + %s -> %s', (step, action, expected) => {
    expect(refusalFor(step as (typeof CHECKLIST_STEPS)[number], action)).toBe(expected);
  });
  it('the table agrees with the spec (4.8): which steps are marked, checked, automatic', () => {
    expect(STEP_KIND).toEqual({ tailnet: 'manual', dns: 'check', bootstrap_vhost: 'manual', certificate: 'check', https_vhost: 'check', enrolled: 'auto', key: 'auto', gateway_test: 'check' });
  });
});

describe('transitions', () => {
  it('mark_done records who-less, plain text; mark_todo clears the detail', () => {
    expect(entryFromManual('mark_done', NOW)).toEqual({ state: 'done', checkedAt: NOW.toISOString(), detail: 'Marked done by an administrator.' });
    expect(entryFromManual('mark_todo', NOW)).toEqual({ state: 'todo', checkedAt: NOW.toISOString(), detail: null });
  });
  it('a check stores the result and the time', () => {
    expect(entryFromCheck({ state: 'failed', detail: 'No A record' }, NOW)).toEqual({ state: 'failed', checkedAt: NOW.toISOString(), detail: 'No A record' });
    expect(entryFromCheck({ state: 'done', detail: 'ok' }, NOW).state).toBe('done');
  });
});
