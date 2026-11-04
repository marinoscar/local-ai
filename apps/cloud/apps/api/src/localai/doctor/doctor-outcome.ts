// Building a Doctor outcome from a cloud code, and the one place text is scrubbed before it can leave the server.
//
// A check's result is shown to every administrator and goes into support bundles, so nothing secret may reach it:
// `scrubText` removes the shapes of the things this app handles (a 64-hex key or digest, a `nod_`/`pat_`/`lai_` token,
// an IPv4 address) from every string an outcome carries, whatever produced the string (a node id typed by an admin, an
// error message, a version printed by a binary).
import type { DoctorCheckOutcome, DoctorDataValue, DoctorStatus } from '@marinoscar/platform-api/doctor';

import type { CloudCode } from './codes';

const REDACTED = '[redacted]';
const HEX_64 = /\b[0-9a-fA-F]{64}\b/g;
const TOKEN = /\b(?:nod|pat|lai)_[A-Za-z0-9_-]{6,}/g;
const IPV4 = /\b(?:\d{1,3}\.){3}\d{1,3}\b/g;

/** The text with every key-, token- or address-shaped part replaced. */
export function scrubText(text: string): string {
  return text.replace(HEX_64, REDACTED).replace(TOKEN, REDACTED).replace(IPV4, REDACTED);
}

export interface OutcomeInput {
  status: DoctorStatus;
  /** The code that explains a `warn` or `fail`; its title leads the detail and its fix is the remedy. */
  code?: CloudCode;
  /** What was found, specifically ("2 nodes: ai-n4, ai-n5"). Follows the code's title. */
  found?: string;
  /** Replaces the whole detail (a `pass`, or a `warn` without a code). */
  detail?: string;
  /** The remedy when there is no code; with a code, this follows the code's fix. */
  remedy?: string;
  /** The error NAME (never a message) of a failed probe. */
  error?: string;
  data?: Record<string, DoctorDataValue>;
}

/** The outcome every check returns: one line, a remedy on `warn`/`fail`, scrubbed, the code in `data.code`. */
export function buildOutcome(input: OutcomeInput): DoctorCheckOutcome {
  const { status, code } = input;
  const lead = code ? `${code.title} [${code.code}]` : undefined;
  const detail = input.detail ?? [lead, input.found].filter(Boolean).join(': ');
  const remedyParts = [code?.fix, input.remedy].filter((part): part is string => Boolean(part));
  const data: Record<string, DoctorDataValue> = { ...(input.data ?? {}), ...(code ? { code: code.code } : {}) };

  return {
    status,
    detail: scrubText(detail),
    ...(status === 'warn' || status === 'fail' ? { remedy: scrubText(remedyParts.join('. ')) } : {}),
    ...(input.error ? { error: scrubText(input.error) } : {}),
    ...(Object.keys(data).length > 0
      ? { data: Object.fromEntries(Object.entries(data).map(([key, value]) => [key, typeof value === 'string' ? scrubText(value) : value])) }
      : {}),
  };
}

/** `warning` is a `warn`; `error` and `critical` are a `fail`. */
export function statusForSeverity(severity: CloudCode['severity']): DoctorStatus {
  return severity === 'error' || severity === 'critical' ? 'fail' : 'warn';
}

/** A failed probe's error NAME: never its message (a decryption error's message is not ours to trust). */
export function errorName(error: unknown): string {
  return error instanceof Error && error.name ? error.name : 'Error';
}

/** "ai-n4, ai-n5 and 3 more" for a list of ids, capped so the line stays short. */
export function nameList(names: readonly string[], max = 5): string {
  if (names.length <= max) return names.join(', ');
  return `${names.slice(0, max).join(', ')} and ${names.length - max} more`;
}
