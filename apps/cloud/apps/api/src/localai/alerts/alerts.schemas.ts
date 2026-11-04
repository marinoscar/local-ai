// Query strings and the response shape of the alert routes (docs/specs/cloud-app.md section 3.9), validated with Zod.
import { BadRequestException } from '@nestjs/common';
import { z } from 'zod';

import { plainFix, type AlertSeverity, type AlertState } from './alert-kinds';
import type { AlertRecord } from './alert-store';

export const ListAlertsQuerySchema = z
  .object({
    state: z.enum(['active', 'cleared', 'all']).default('active'),
    nodeId: z.string().regex(/^ai-n\d{1,6}$/, 'nodeId must look like ai-n3').optional(),
    limit: z.coerce.number().int().min(1).max(200).default(100),
  })
  .strict();
export type ListAlertsQuery = z.infer<typeof ListAlertsQuerySchema>;

export const AlertIdSchema = z.string().uuid();

/** One alert as the API returns it. `details` is deliberately not included; the fix is the plain-language text. */
export interface AlertDto {
  id: string;
  /** The lab node id (`ai-n2`), or null for a lab-wide alert. */
  nodeId: string | null;
  nodeName: string | null;
  kind: string;
  code: string;
  severity: AlertSeverity;
  state: AlertState;
  title: string;
  fix: string;
  firstRaisedAt: string;
  lastSeenAt: string;
  clearedAt: string | null;
  acknowledgedAt: string | null;
}

export interface AlertsResponse {
  alerts: AlertDto[];
}

export function toAlertDto(record: AlertRecord): AlertDto {
  return {
    id: record.id,
    nodeId: record.nodeId,
    nodeName: record.nodeName,
    kind: record.kind,
    code: record.code,
    severity: record.severity,
    state: record.state,
    title: record.title,
    fix: plainFix(record.code),
    firstRaisedAt: record.firstRaisedAt.toISOString(),
    lastSeenAt: record.lastSeenAt.toISOString(),
    clearedAt: record.clearedAt ? record.clearedAt.toISOString() : null,
    acknowledgedAt: record.acknowledgedAt ? record.acknowledgedAt.toISOString() : null,
  };
}

/** Parses with `schema`; a failure is a 400 in the platform's error format with the Zod issues. */
export function parseWith<T extends z.ZodType>(schema: T, value: unknown): z.infer<T> {
  const parsed = schema.safeParse(value ?? {});
  if (!parsed.success) {
    throw new BadRequestException({ message: 'The request is not valid', details: { issues: parsed.error.issues.map((issue) => ({ path: issue.path.join('.'), message: issue.message })) } });
  }
  return parsed.data;
}
