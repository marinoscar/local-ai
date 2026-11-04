// The plain-language line for a doctor code (docs/specs/doctor-codes.md), from the generated copy of the registry.
// A code that is not in the copy falls back to the message the agent sent (already scrubbed), then to a pointer.
import { DOCTOR_FIXES } from './doctorFixes.generated';

export const CODE_PATTERN = /^LAI-[A-Z]{2,3}-\d{3}$/;

export interface PlainError {
  code: string | null;
  /** What happened, in plain words. */
  meaning: string | null;
  /** What to do about it. */
  fix: string;
}

const GENERIC_FIX = "Open the node's page and try again; if it keeps failing, run `aictl doctor` on the computer.";

/** The registry row of `code`, or null. */
export const registryRow = (code: string | null | undefined) => (code && CODE_PATTERN.test(code) ? (DOCTOR_FIXES[code] ?? null) : null);

/** What to show for a stored `{ code, message }` error: the registry's meaning and fix, else the agent's own message. */
export function plainError(error: { code?: string | null; message?: string | null } | null | undefined): PlainError {
  const code = error?.code && CODE_PATTERN.test(error.code) ? error.code : null;
  const row = registryRow(code);
  const message = typeof error?.message === 'string' ? error.message.trim() : '';
  if (row) return { code, meaning: row.meaning, fix: row.fix };
  if (message) return { code, meaning: null, fix: message };
  return { code, meaning: null, fix: GENERIC_FIX };
}
