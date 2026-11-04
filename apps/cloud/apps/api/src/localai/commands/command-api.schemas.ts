// Request bodies and queries of the command routes for the UI (docs/specs/cloud-app.md section 3.9), Zod-validated.
// The params of each command type are NOT validated here: `CommandsService.request` runs the type's own strict
// schema from the registry, so there is one definition of what a command carries.
import { BadRequestException } from '@nestjs/common';
import { z } from 'zod';

import { unknownCommand } from './command-errors';

export const RequestCommandBodySchema = z
  .object({
    /** `model.load` or `localai.model.load`; the service resolves it against the closed registry. */
    type: z.string().min(1).max(64),
    params: z.record(z.string(), z.unknown()).default({}),
  })
  .strict();
export type RequestCommandBody = z.infer<typeof RequestCommandBodySchema>;

export const COMMAND_STATUSES = ['queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled'] as const;

export const ListCommandsQuerySchema = z
  .object({
    status: z.enum(COMMAND_STATUSES).optional(),
    page: z.coerce.number().int().min(1).max(100_000).default(1),
    pageSize: z.coerce.number().int().min(1).max(100).default(20),
  })
  .strict();
export type ListCommandsQuery = z.infer<typeof ListCommandsQuerySchema>;

/** Parses a body or query with `schema`; a failure is a 400 in the platform's error format with the Zod issues. */
export function parseWith<T extends z.ZodType>(schema: T, value: unknown): z.infer<T> {
  const parsed = schema.safeParse(value ?? {});
  if (!parsed.success) {
    throw new BadRequestException({
      message: 'The request is not valid.',
      details: { reason: 'invalid_request', issues: parsed.error.issues.map((issue) => ({ path: issue.path.join('.'), code: issue.code, message: issue.message })) },
    });
  }
  return parsed.data;
}

const CommandIdSchema = z.string().uuid();

/** The command id from a path, or a 404 for something that is not a uuid (it never reaches SQL). */
export function commandIdOf(raw: string): string {
  const parsed = CommandIdSchema.safeParse(raw);
  if (!parsed.success) throw unknownCommand();
  return parsed.data;
}
