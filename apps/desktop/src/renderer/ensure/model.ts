// The ensure window's texts and decisions (docs/specs/desktop.md 4.12, ADR-0010). Pure data and functions: the page and its
// unit test import it. The agent already writes the state sentence, the next step and every part's title in the UI language
// (the main process sends Accept-Language), so only the window's own words are here.
import type { EnsureResult, EnsureSnapshot, EnsureState, EnsureStepStatus, EnsureStepView, Lang } from '../../shared/types';

interface Bilingual {
  en: string;
  es: string;
}

export const POLL_BUSY_MS = 1000;
export const POLL_IDLE_MS = 5000;

export const STRINGS = {
  title: { en: "Install what's missing", es: 'Instalar lo que falta' },
  intro: {
    en: 'This computer keeps itself complete from the rebuild kit. Here you can see what is missing and start the install. It works without internet.',
    es: 'Esta computadora se mantiene completa con el kit de reconstrucción. Aquí ves lo que falta y puedes iniciar la instalación. Funciona sin internet.',
  },
  stateComplete: { en: 'Complete', es: 'Completa' },
  stateInstalling: { en: 'Installing', es: 'Instalando' },
  stateNeedsYou: { en: 'Needs you', es: 'Te necesita' },
  stateOff: { en: 'Automatic install is off', es: 'La instalación automática está apagada' },
  next: { en: 'What to do next', es: 'Qué hacer ahora' },
  install: { en: "Install what's missing", es: 'Instalar lo que falta' },
  working: { en: 'Working on it…', es: 'Trabajando en ello…' },
  stop: { en: 'Stop installing', es: 'Detener la instalación' },
  stopped: { en: 'Stopped. What was already installed stays.', es: 'Detenido. Lo que ya se instaló se queda.' },
  started: { en: 'The install has started. You can close this window; it keeps going.', es: 'Comenzó la instalación. Puedes cerrar esta ventana; sigue sola.' },
  step: { en: 'Step {n} of {total}', es: 'Paso {n} de {total}' },
  parts: { en: 'Each part', es: 'Cada parte' },
  approve: { en: 'Approve…', es: 'Aprobar…' },
  needsOk: { en: 'Needs your OK', es: 'Necesita tu aprobación' },
  confirmTitle: { en: 'Approve this install?', es: '¿Aprobar esta instalación?' },
  confirmBody: {
    en: 'These parts never start on their own. Read what each one does, then approve with the administrator PIN.',
    es: 'Estas partes nunca comienzan solas. Lee lo que hace cada una y aprueba con el PIN de administrador.',
  },
  confirmYes: { en: 'Approve with PIN', es: 'Aprobar con PIN' },
  confirmNo: { en: 'Not now', es: 'Ahora no' },
  warnBuild: {
    en: 'It builds the speech program from source. It can take an hour or more and uses the whole processor, so the computer will be slow meanwhile.',
    es: 'Compila el programa de voz desde el código. Puede tardar una hora o más y usa todo el procesador, así que la computadora irá lenta mientras tanto.',
  },
  warnRestartKiwix: {
    en: 'It restarts the library for a few seconds. Anyone reading a book will lose their place.',
    es: 'Reinicia la biblioteca unos segundos. Quien esté leyendo un libro perderá su lugar.',
  },
  fix: { en: 'Fix', es: 'Solución' },
  unreachable: { en: 'This computer cannot say what is missing right now.', es: 'Esta computadora no puede decir ahora qué le falta.' },
  unreachableFix: { en: 'Wait a minute and it will try again by itself.', es: 'Espera un minuto y lo intentará de nuevo sola.' },
  failed: { en: 'That did not work.', es: 'No funcionó.' },
  failedFix: { en: 'Try again. If it keeps failing, run in a terminal: aictl ensure --dry-run', es: 'Inténtalo de nuevo. Si sigue fallando, ejecuta en una terminal: aictl ensure --dry-run' },
  typed: { en: 'Or type in a terminal: aictl ensure', es: 'O escribe en una terminal: aictl ensure' },
  close: { en: 'Close', es: 'Cerrar' },
  loading: { en: 'Loading…', es: 'Cargando…' },
  statusOk: { en: 'Already installed', es: 'Ya instalado' },
  statusPending: { en: 'Next in line', es: 'En espera de turno' },
  statusWaiting: { en: 'Waiting for the button', es: 'Espera el botón' },
  statusNeedsConfirm: { en: 'Needs your OK', es: 'Necesita tu aprobación' },
  statusBlocked: { en: 'Cannot be done yet', es: 'Aún no se puede' },
  statusRunning: { en: 'Installing…', es: 'Instalando…' },
  statusDone: { en: 'Installed', es: 'Instalado' },
  statusFailed: { en: 'Failed', es: 'Falló' },
  statusCancelled: { en: 'Stopped', es: 'Detenido' },
} as const satisfies Record<string, Bilingual>;

export const pick = (text: Bilingual, lang: Lang): string => text[lang];
export const fill = (text: string, values: Record<string, string | number>): string => text.replace(/\{(\w+)\}/g, (m, name: string) => (name in values ? String(values[name]) : m));

const STATE_TEXT: Record<EnsureState, Bilingual> = {
  complete: STRINGS.stateComplete,
  installing: STRINGS.stateInstalling,
  needs_you: STRINGS.stateNeedsYou,
  off: STRINGS.stateOff,
};

/** Colour is never the only signal: each state also has a mark. */
export const STATE_MARK: Record<EnsureState, string> = { complete: '✓', installing: '…', needs_you: '!', off: '!' };
export const stateText = (state: EnsureState, lang: Lang): string => pick(STATE_TEXT[state], lang);
export type Severity = 'success' | 'info' | 'warning';
export const stateSeverity = (state: EnsureState): Severity => (state === 'complete' ? 'success' : state === 'installing' ? 'info' : 'warning');

const STATUS_TEXT: Record<EnsureStepStatus, Bilingual> = {
  ok: STRINGS.statusOk,
  pending: STRINGS.statusPending,
  waiting: STRINGS.statusWaiting,
  needs_confirm: STRINGS.statusNeedsConfirm,
  blocked: STRINGS.statusBlocked,
  running: STRINGS.statusRunning,
  done: STRINGS.statusDone,
  failed: STRINGS.statusFailed,
  cancelled: STRINGS.statusCancelled,
};
export const statusText = (status: EnsureStepStatus, lang: Lang): string => pick(STATUS_TEXT[status], lang);

/** What a step that needs a person's OK costs (the confirmation says what is lost and how long it takes). Empty for any other step. */
export function warningFor(stepId: string, lang: Lang): string {
  if (stepId === 'whisper.build') return pick(STRINGS.warnBuild, lang);
  if (stepId === 'restart.kiwix') return pick(STRINGS.warnRestartKiwix, lang);
  return '';
}

/** The seconds between two reads of the picture: fast while a pass runs, slow otherwise. */
export const pollDelay = (view: EnsureSnapshot | null): number => (view?.state === 'installing' ? POLL_BUSY_MS : POLL_IDLE_MS);

/** The steps a person has to approve (status `needs_confirm`), in the order of the picture. */
export const stepsNeedingOk = (view: EnsureSnapshot): EnsureStepView[] => view.steps.filter((s) => s.status === 'needs_confirm');

/** The button "Install what's missing" is useful only when something is left, no pass runs and the agent will do it on a press. */
export function canInstall(view: EnsureSnapshot): boolean {
  if (view.state === 'installing' || view.state === 'complete') return false;
  return view.steps.some((s) => s.status === 'pending' || s.status === 'waiting' || s.status === 'failed');
}

/** The "Step n of total" line: n is the step running (done + 1), never beyond the total. */
export function progressText(view: EnsureSnapshot, lang: Lang): string {
  return fill(pick(STRINGS.step, lang), { n: Math.min(view.progress.done + 1, view.progress.total), total: view.progress.total });
}

export interface Problem {
  text: string;
  fix: string;
  code?: string;
}

/**
 * What the window says about a refusal. The agent's own message and fix (already in the UI language) win; without them the
 * window's words are used. A closed PIN window is not a problem: the person decided.
 */
export function describeFailure(result: Extract<EnsureResult, { ok: false }>, lang: Lang, fallback: 'view' | 'action'): Problem | null {
  if (result.cancelled) return null;
  const text = result.message ? result.message : pick(fallback === 'view' ? STRINGS.unreachable : STRINGS.failed, lang);
  const fix = result.fix ? result.fix : pick(fallback === 'view' ? STRINGS.unreachableFix : STRINGS.failedFix, lang);
  return { text, fix, ...(result.code.startsWith('LAI-') ? { code: result.code } : {}) };
}
