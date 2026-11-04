// The publish/unpublish journal as data (docs/specs/cloud-app.md section 4.10): how it is kept in
// `localai_nodes.publish_checklist` (under the reserved key `vps`, beside the six operator-driven steps) and how a
// journal moves the wizard's checklist steps 2 to 5 (`dns`, `bootstrap_vhost`, `certificate`, `https_vhost`).
// Pure: no I/O, so every transition is table-tested.
import type { StoredStep } from '../wizard/checklist.state';
import type { ChecklistStep } from '../wizard/wizard.schemas';
import { scrubVpsText } from './vps-scrub';
import type { StoredJournal } from './vps-result.schema';
import { VPS_JOURNAL_KEY } from './vps.constants';

/** The checklist steps the stack-agent completes (steps 2 to 5 of the wizard). */
export const AUTOMATED_CHECKLIST_STEPS = ['dns', 'bootstrap_vhost', 'certificate', 'https_vhost'] as const satisfies readonly ChecklistStep[];
export type AutomatedChecklistStep = (typeof AUTOMATED_CHECKLIST_STEPS)[number];

/** Which journal steps decide each checklist step. All of them must be present and none failed for `done`. */
const PUBLISH_MAP: Readonly<Record<AutomatedChecklistStep, readonly string[]>> = {
  dns: ['dns_upsert', 'dns_wait'],
  bootstrap_vhost: ['bootstrap_vhost'],
  certificate: ['certificate'],
  https_vhost: ['final_vhost', 'validate'],
};

export type ChecklistUpdates = Partial<Record<AutomatedChecklistStep, StoredStep>>;

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** The kept journal, read defensively. Null when absent or malformed. */
export function readJournal(publishChecklist: unknown): StoredJournal | null {
  if (!isRecord(publishChecklist)) return null;
  const raw = publishChecklist[VPS_JOURNAL_KEY];
  if (!isRecord(raw) || typeof raw.jobId !== 'string' || typeof raw.host !== 'string' || !Array.isArray(raw.steps)) return null;
  if (raw.action !== 'publish' && raw.action !== 'unpublish') return null;
  if (raw.outcome !== 'done' && raw.outcome !== 'failed') return null;
  const steps: StoredJournal['steps'] = [];
  for (const step of raw.steps) {
    if (!isRecord(step) || typeof step.name !== 'string' || typeof step.detail !== 'string') continue;
    if (step.status !== 'done' && step.status !== 'skipped' && step.status !== 'failed') continue;
    steps.push({ name: step.name, status: step.status, detail: scrubVpsText(step.detail) });
  }
  return {
    jobId: raw.jobId,
    action: raw.action,
    host: raw.host,
    outcome: raw.outcome,
    finishedAt: typeof raw.finishedAt === 'string' ? raw.finishedAt : '',
    steps,
    certificateExpiresAt: typeof raw.certificateExpiresAt === 'string' ? raw.certificateExpiresAt : null,
  };
}

/** The reserved entries of a stored checklist, to be carried over when the six operator steps are rewritten. */
export function reservedEntries(publishChecklist: unknown): Record<string, unknown> {
  if (!isRecord(publishChecklist) || !(VPS_JOURNAL_KEY in publishChecklist)) return {};
  return { [VPS_JOURNAL_KEY]: publishChecklist[VPS_JOURNAL_KEY] };
}

/** Builds the journal to keep from a validated result. Every string is scrubbed again. */
export function buildJournal(args: {
  jobId: string;
  action: 'publish' | 'unpublish';
  host: string;
  steps: ReadonlyArray<{ name: string; status: 'done' | 'skipped' | 'failed'; detail: string }>;
  certificateExpiresAt: string | null;
  now: Date;
}): StoredJournal {
  const steps = args.steps.map((step) => ({ name: step.name, status: step.status, detail: scrubVpsText(step.detail) }));
  return {
    jobId: args.jobId,
    action: args.action,
    host: args.host,
    outcome: steps.some((step) => step.status === 'failed') ? 'failed' : 'done',
    finishedAt: args.now.toISOString(),
    steps,
    certificateExpiresAt: args.certificateExpiresAt,
  };
}

/** The first failed step of a journal, if any. */
export function failedStep(journal: Pick<StoredJournal, 'steps'>): { name: string; detail: string } | null {
  const failed = journal.steps.find((step) => step.status === 'failed');
  return failed ? { name: failed.name, detail: failed.detail } : null;
}

/**
 * How a finished journal changes the checklist.
 *  - publish: a step whose journal steps are all present and none failed becomes `done`; one with a failed journal step
 *    becomes `failed` with that step's detail; one the run never reached is left as it was.
 *  - unpublish, when it finished without a failure: all four become `todo` again. A failed unpublish changes nothing.
 */
export function checklistUpdates(journal: StoredJournal, now: Date): ChecklistUpdates {
  const checkedAt = now.toISOString();
  const updates: ChecklistUpdates = {};
  if (journal.action === 'unpublish') {
    if (journal.outcome === 'failed') return updates;
    for (const step of AUTOMATED_CHECKLIST_STEPS) updates[step] = { state: 'todo', checkedAt, detail: 'Unpublished by the VPS stack-agent.' };
    return updates;
  }
  for (const step of AUTOMATED_CHECKLIST_STEPS) {
    const mapped = journal.steps.filter((entry) => PUBLISH_MAP[step].includes(entry.name));
    const failed = mapped.find((entry) => entry.status === 'failed');
    if (failed) updates[step] = { state: 'failed', checkedAt, detail: scrubVpsText(failed.detail, 1000) };
    else if (mapped.length === PUBLISH_MAP[step].length) updates[step] = { state: 'done', checkedAt, detail: 'Done automatically by the VPS stack-agent.' };
  }
  return updates;
}
