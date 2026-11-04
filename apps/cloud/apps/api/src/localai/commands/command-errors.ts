// The refusals of the command plane in EAB's error format: the HTTP status and message are EAB's, and our doctor
// code and a machine-readable reason travel in `details` (the same shape as the binding guard's, node-link.errors.ts).
// A message is plain language and never carries a claim token, params or result text.
import { BadRequestException, ConflictException, NotFoundException } from '@nestjs/common';
import type { ZodError } from 'zod';

import { LAI_CLD_LEASE_LOST } from './command-failure';

/** A validation issue as the agent reads it in `details.issues`: where, what kind, and why. Never the offending value. */
export interface WireIssue {
  path: Array<string | number>;
  code: string;
  message: string;
}

export function wireIssues(error: ZodError): WireIssue[] {
  return error.issues.map((issue) => ({ path: issue.path.map((part) => (typeof part === 'symbol' ? String(part) : part)), code: issue.code, message: issue.message }));
}

/** `409`: the server moved on (lease lost, already settled, not this node's command). The agent drops the work. */
export function leaseLost(): ConflictException {
  return new ConflictException({
    message: 'This command is no longer held by this node',
    details: { code: LAI_CLD_LEASE_LOST, reason: 'lease_lost' },
  });
}

/** `400` for a body that does not match its schema. */
export function invalidBody(error: ZodError): BadRequestException {
  return new BadRequestException({
    message: 'The request body is not valid',
    details: { reason: 'invalid_body', issues: wireIssues(error) },
  });
}

/** `400` for a result posted under another type than the command's. The command stays running. */
export function resultTypeMismatch(): BadRequestException {
  return new BadRequestException({
    message: 'The result type does not match the command',
    details: { reason: 'result_type_mismatch' },
  });
}

/** `400` for a result that fails its type's schema. The command stays running. */
export function invalidResult(error: ZodError): BadRequestException {
  return new BadRequestException({
    message: 'The result does not match the command type',
    details: { reason: 'invalid_result', issues: wireIssues(error) },
  });
}

/** `404` for a command id that is not a uuid (it never reaches SQL). */
export function unknownCommand(): NotFoundException {
  return new NotFoundException({ message: 'No such command', details: { reason: 'unknown_command' } });
}

/** `400` for enqueueing a type or params the closed registry does not accept (CLOUD-09 maps it for the UI). */
export function invalidCommandParams(error: ZodError | null, type: string): BadRequestException {
  return new BadRequestException({
    message: error ? 'The command parameters are not valid' : 'Unknown command type',
    details: error ? { reason: 'invalid_params', type, issues: wireIssues(error) } : { reason: 'unknown_command_type', type },
  });
}
