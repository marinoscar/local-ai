// The refusals of `POST /api/nodes/{id}/localai/state` (docs/specs/cloud-app.md sections 3.6 and 4.3) in EAB's error
// format: HTTP status and message are EAB's, our doctor code and a machine-readable reason travel in `details`, where
// the agent reads them (`details.code`).
//
// A message is plain language for an operator and NEVER carries a value from the request body: not a secret-looking
// string, not an unknown key name, not the `received` text of a Zod issue.
import { BadRequestException, ConflictException } from '@nestjs/common';
import type { ZodError } from 'zod';

import { LAI_CLD_WRONG_NODE } from './node-link.errors';

/** "The cloud refused this computer's status report" (docs/specs/doctor-codes.md). */
export const LAI_CLD_STATE_REFUSED = 'LAI-CLD-008';

/** Every `details.reason` the state route can give (permanent strings; the agent matches on them). */
export type StateRefusalReason = 'stale_seq' | 'secret_like_value' | 'schema' | 'node_id_mismatch';

/** A Zod issue reduced to what is safe to echo: where it is and what kind it is, never the received value. */
export interface SafeStateIssue {
  path: string;
  code: string;
}

/** Longest list of issues returned (the first ones are the useful ones). */
const MAX_ISSUES = 20;

function pathText(path: ReadonlyArray<PropertyKey>): string {
  let text = '';
  for (const segment of path) {
    if (typeof segment === 'number') text += `[${segment}]`;
    else if (/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(String(segment))) text += text === '' ? String(segment) : `.${String(segment)}`;
    else text += text === '' ? '<key>' : '.<key>';
  }
  return text === '' ? '$' : text;
}

/** Zod issues as `{ path, code }`. An `unrecognized_keys` issue reports the object's path, not the key names. */
export function safeIssues(error: ZodError): SafeStateIssue[] {
  return error.issues.slice(0, MAX_ISSUES).map((issue) => ({ path: pathText(issue.path), code: issue.code }));
}

/** 400: the body is not a `LocalaiStateSnapshotV1`. */
export function schemaRefusal(issues: SafeStateIssue[]): BadRequestException {
  return new BadRequestException({
    message: 'The status report does not match the expected format',
    details: { code: LAI_CLD_STATE_REFUSED, reason: 'schema' satisfies StateRefusalReason, issues },
  });
}

/** 400: a string in the body looks like a secret, an address or a home path. Only the JSON path is returned. */
export function secretRefusal(path: string): BadRequestException {
  return new BadRequestException({
    message: 'The status report contains a value that looks like a secret or an address and was refused',
    details: { code: LAI_CLD_STATE_REFUSED, reason: 'secret_like_value' satisfies StateRefusalReason, path },
  });
}

/** 400 LAI-CLD-004: the snapshot speaks for another node than the one the credential is bound to. */
export function nodeIdMismatch(): BadRequestException {
  return new BadRequestException({
    message: 'The status report is for a different node than this credential belongs to',
    details: { code: LAI_CLD_WRONG_NODE, reason: 'node_id_mismatch' satisfies StateRefusalReason },
  });
}

/** 409: not newer than the stored snapshot of this agent boot. The agent ignores it. */
export function staleSeq(): ConflictException {
  return new ConflictException({
    message: 'A newer status report was already accepted',
    details: { code: LAI_CLD_STATE_REFUSED, reason: 'stale_seq' satisfies StateRefusalReason },
  });
}
