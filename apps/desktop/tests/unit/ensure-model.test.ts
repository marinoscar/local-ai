// The ensure window's texts and decisions (issue #480, docs/specs/desktop.md 4.12): both languages, the button rule, the polling pace
// and what is said about a refusal.
import { describe, expect, it } from 'vitest';
import { ENSURE_CONFIRM_STEPS, type EnsureSnapshot, type EnsureStepStatus, type EnsureStepView } from '../../src/shared/types';
import { POLL_BUSY_MS, POLL_IDLE_MS, STATE_MARK, STRINGS, canInstall, describeFailure, fill, pollDelay, progressText, stateSeverity, stateText, statusText, stepsNeedingOk, warningFor } from '../../src/renderer/ensure/model';

const step = (id: string, status: EnsureStepStatus): EnsureStepView => ({ id, status, title: id, detail: '', needsConfirmation: status === 'needs_confirm' });
const snapshot = (state: EnsureSnapshot['state'], steps: EnsureStepView[], done = 0): EnsureSnapshot => ({
  state,
  auto: 'on',
  message: state,
  nextAction: { kind: 'none', text: '' },
  progress: { done, total: steps.length },
  steps,
});

describe('strings', () => {
  it('every string exists in both languages and Spanish differs from English', () => {
    for (const [name, text] of Object.entries(STRINGS)) {
      expect(text.en, name).not.toBe('');
      expect(text.es, name).not.toBe('');
    }
    expect(STRINGS.install).toEqual({ en: "Install what's missing", es: 'Instalar lo que falta' });
    expect(STRINGS.stop.es).not.toBe(STRINGS.stop.en);
  });

  it('keeps the same placeholders in both languages', () => {
    const placeholders = (s: string): string[] => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]!).sort();
    for (const [name, text] of Object.entries(STRINGS)) expect(placeholders(text.es), name).toEqual(placeholders(text.en));
  });

  it('every state and step status has a name in both languages, and a state has a mark', () => {
    for (const state of ['complete', 'installing', 'needs_you', 'off'] as const) {
      expect(stateText(state, 'en')).not.toBe('');
      expect(stateText(state, 'es')).not.toBe('');
      expect(STATE_MARK[state]).not.toBe('');
    }
    for (const status of ['ok', 'pending', 'waiting', 'needs_confirm', 'blocked', 'running', 'done', 'failed', 'cancelled'] as const) {
      expect(statusText(status, 'en')).not.toBe('');
      expect(statusText(status, 'es')).not.toBe('');
    }
    expect(stateSeverity('complete')).toBe('success');
    expect(stateSeverity('installing')).toBe('info');
    expect(stateSeverity('needs_you')).toBe('warning');
  });

  it('says what each confirmable step costs, in both languages, and nothing for another step', () => {
    for (const id of ENSURE_CONFIRM_STEPS) {
      expect(warningFor(id, 'en'), id).not.toBe('');
      expect(warningFor(id, 'es'), id).not.toBe('');
    }
    expect(warningFor('whisper.build', 'en')).toContain('hour');
    expect(warningFor('restart.kiwix', 'es')).toContain('biblioteca');
    expect(warningFor('tool.piper', 'en')).toBe('');
  });

  it('fill substitutes and keeps an unknown placeholder visible', () => {
    expect(fill('Step {n} of {total}', { n: 2, total: 4 })).toBe('Step 2 of 4');
    expect(fill('{x}', {})).toBe('{x}');
  });
});

describe('the button rule', () => {
  it('is on when a step waits for the button or failed, and no pass runs', () => {
    expect(canInstall(snapshot('needs_you', [step('tool.piper', 'pending')]))).toBe(true);
    expect(canInstall(snapshot('off', [step('tool.piper', 'waiting')]))).toBe(true);
    expect(canInstall(snapshot('needs_you', [step('tool.piper', 'failed')]))).toBe(true);
  });
  it('is off while installing, when complete, and when only an approval or the kit is missing', () => {
    expect(canInstall(snapshot('installing', [step('tool.piper', 'running')]))).toBe(false);
    expect(canInstall(snapshot('complete', [step('tool.piper', 'ok')]))).toBe(false);
    expect(canInstall(snapshot('needs_you', [step('whisper.build', 'needs_confirm'), step('tool.pmtiles', 'blocked')]))).toBe(false);
  });
  it('lists the steps that need a person in the order of the picture', () => {
    expect(stepsNeedingOk(snapshot('needs_you', [step('restart.kiwix', 'needs_confirm'), step('tool.piper', 'pending'), step('whisper.build', 'needs_confirm')])).map((s) => s.id)).toEqual(['restart.kiwix', 'whisper.build']);
  });
});

describe('progress and polling', () => {
  it('says which step runs, never beyond the total', () => {
    expect(progressText(snapshot('installing', [step('a', 'done'), step('b', 'running'), step('c', 'pending')], 1), 'en')).toBe('Step 2 of 3');
    expect(progressText(snapshot('installing', [step('a', 'done')], 1), 'es')).toBe('Paso 1 de 1');
  });
  it('asks every second while installing and every five seconds otherwise', () => {
    expect(pollDelay(snapshot('installing', []))).toBe(POLL_BUSY_MS);
    expect(pollDelay(snapshot('complete', []))).toBe(POLL_IDLE_MS);
    expect(pollDelay(null)).toBe(POLL_IDLE_MS);
    expect(POLL_BUSY_MS).toBe(1000);
  });
});

describe('describeFailure', () => {
  it('shows the agent message, fix and code', () => {
    expect(describeFailure({ ok: false, code: 'LAI-SYN-005', message: 'A copy is running', fix: 'Try again when it ends.' }, 'en', 'action')).toEqual({ text: 'A copy is running', fix: 'Try again when it ends.', code: 'LAI-SYN-005' });
  });
  it('uses the window words when the agent said nothing, in the language asked', () => {
    expect(describeFailure({ ok: false, code: 'LAI-DSK-004' }, 'es', 'view')).toEqual({ text: STRINGS.unreachable.es, fix: STRINGS.unreachableFix.es, code: 'LAI-DSK-004' });
    expect(describeFailure({ ok: false, code: 'unavailable' }, 'en', 'action')).toEqual({ text: STRINGS.failed.en, fix: STRINGS.failedFix.en });
  });
  it('says nothing when the person closed the PIN window', () => {
    expect(describeFailure({ ok: false, code: 'LAI-SEC-002', cancelled: true }, 'en', 'action')).toBeNull();
  });
});
