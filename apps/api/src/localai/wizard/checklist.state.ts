// The publish checklist as data (docs/specs/cloud-app.md section 4.8): which steps exist, how each is completed, how
// the stored JSON is read and how an operator's action changes it. Pure: no I/O, so every transition is table-tested.
import { z } from 'zod';

import { CHECKLIST_STEPS, type ChecklistActionBody, type ChecklistStep } from './wizard.schemas';

export type StepState = 'todo' | 'done' | 'failed';

/** How a step is completed: by the operator saying so, by a Check button, or by the cloud itself. */
export type StepKind = 'manual' | 'check' | 'auto';

export const STEP_KIND: Readonly<Record<ChecklistStep, StepKind>> = {
  tailnet: 'manual',
  dns: 'check',
  bootstrap_vhost: 'manual',
  certificate: 'check',
  https_vhost: 'check',
  enrolled: 'auto',
  key: 'auto',
  gateway_test: 'check',
};

/** One stored step: `publish_checklist[step]`. */
export interface StoredStep {
  state: StepState;
  checkedAt: string | null;
  detail: string | null;
}

export type StoredChecklist = Partial<Record<ChecklistStep, StoredStep>>;

const StoredStepSchema = z.object({
  state: z.enum(['todo', 'done', 'failed']),
  checkedAt: z.string().nullable().optional().catch(null),
  detail: z.string().max(1000).nullable().optional().catch(null),
});

/** Reads the stored JSON defensively: an unknown step or a malformed entry is dropped, never thrown. */
export function parseStoredChecklist(value: unknown): StoredChecklist {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return {};
  const out: StoredChecklist = {};
  for (const step of CHECKLIST_STEPS) {
    const parsed = StoredStepSchema.safeParse((value as Record<string, unknown>)[step]);
    if (parsed.success) out[step] = { state: parsed.data.state, checkedAt: parsed.data.checkedAt ?? null, detail: parsed.data.detail ?? null };
  }
  return out;
}

/** What the cloud itself knows about a node, for the two automatic steps. */
export interface AutoFacts {
  phase: 'pending' | 'enrolled' | 'retired';
  keyReportedAt: Date | null;
}

export interface ChecklistStepView {
  step: ChecklistStep;
  order: number;
  kind: StepKind;
  state: StepState;
  checkedAt: string | null;
  detail: string | null;
}

/** The eight steps in order, with the automatic ones computed from the node and the rest from the stored JSON. */
export function viewSteps(stored: StoredChecklist, facts: AutoFacts): ChecklistStepView[] {
  return CHECKLIST_STEPS.map((step, index) => {
    const kind = STEP_KIND[step];
    if (step === 'enrolled') {
      const done = facts.phase === 'enrolled';
      return { step, order: index + 1, kind, state: done ? 'done' : 'todo', checkedAt: null, detail: done ? 'The node has contacted the cloud.' : 'Waiting for the node to contact the cloud (Part 6 of the runbook).' };
    }
    if (step === 'key') {
      const done = facts.keyReportedAt !== null;
      return {
        step,
        order: index + 1,
        kind,
        state: done ? 'done' : 'todo',
        checkedAt: facts.keyReportedAt?.toISOString() ?? null,
        detail: done ? 'The node’s llama key reached the cloud.' : 'Waiting for the node to send its llama key.',
      };
    }
    const entry = stored[step];
    return { step, order: index + 1, kind, state: entry?.state ?? 'todo', checkedAt: entry?.checkedAt ?? null, detail: entry?.detail ?? null };
  });
}

/** All eight done: the page shows "<node> is part of the lab". */
export function isComplete(steps: readonly ChecklistStepView[]): boolean {
  return steps.length === CHECKLIST_STEPS.length && steps.every((step) => step.state === 'done');
}

export type ActionRefusal = 'automatic_step' | 'not_a_manual_step' | 'not_a_check_step';

/** Whether `action` may be taken on `step`. `check` is applied by the service (it needs the network), not here. */
export function refusalFor(step: ChecklistStep, action: ChecklistActionBody['action']): ActionRefusal | null {
  const kind = STEP_KIND[step];
  if (kind === 'auto') return 'automatic_step';
  if (action === 'mark_done') return kind === 'manual' ? null : 'not_a_manual_step';
  if (action === 'check') return kind === 'check' ? null : 'not_a_check_step';
  return null; // mark_todo resets any manual or check step
}

/** The stored entry after a manual action. `check` goes through `entryFromCheck`. */
export function entryFromManual(action: 'mark_done' | 'mark_todo', now: Date): StoredStep {
  return action === 'mark_done'
    ? { state: 'done', checkedAt: now.toISOString(), detail: 'Marked done by an administrator.' }
    : { state: 'todo', checkedAt: now.toISOString(), detail: null };
}

export function entryFromCheck(result: { state: 'done' | 'failed'; detail: string }, now: Date): StoredStep {
  return { state: result.state, checkedAt: now.toISOString(), detail: result.detail };
}
