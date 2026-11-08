// What the error page shows, as data (docs/specs/desktop.md 4.2 "Buttons instead of commands"), so it is unit-tested
// without a DOM: the button row in order, and whether the typed command leads or is folded under "Use a terminal instead".
import { FIX_ACTIONS, PAGE_STRINGS, type Bilingual, type ErrorAction, ERROR_COPY, isDskCode } from '../../main/i18n';
import type { ActionResult } from '../../shared/types';

export interface ErrorPageLayout {
  /** The buttons of the row, in order. */
  buttons: readonly ErrorAction[];
  /** The command shown above the buttons (a page whose only fix is the command). */
  leadCommand: string | null;
  /** The command folded under "Use a terminal instead", after the buttons (a button fixes it). */
  terminalCommand: string | null;
}

export function errorPageLayout(code: string | null): ErrorPageLayout {
  if (!isDskCode(code)) return { buttons: ['open-portal'], leadCommand: null, terminalCommand: null };
  const copy = ERROR_COPY[code];
  const command = copy.command ?? null;
  // A button fixes it: the typed command is the second way, and its "Copy command" moves into the disclosure.
  if (copy.actions.some((a) => FIX_ACTIONS.includes(a))) {
    return { buttons: copy.actions.filter((a) => a !== 'copy-command'), leadCommand: null, terminalCommand: command };
  }
  return { buttons: copy.actions, leadCommand: command, terminalCommand: null };
}

/** Fallbacks and ways out: outlined, so the fix is the button that stands out. */
export const SECONDARY_ACTIONS: readonly ErrorAction[] = ['open-portal', 'open-browser', 'open-library', 'restart-computer'];

/** Buttons that stay usable while a privileged call waits for its password window. */
export const ALWAYS_USABLE: readonly ErrorAction[] = ['open-portal', 'open-browser', 'open-library'];

/** The note a failed privileged button shows (allow-user, rotate-token). Null: nothing to say (another one is running). */
export function fixFailureNote(result: ActionResult): Bilingual | null {
  if (result.busy === true) return null;
  if (result.reason === 'cancelled') return PAGE_STRINGS.fixCancelled;
  if (result.reason === 'unavailable') return PAGE_STRINGS.fixUnavailable;
  return PAGE_STRINGS.fixFailed;
}
