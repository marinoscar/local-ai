// The body of `PUT /api/localai/settings` and the checks that need the key table (docs/specs/cloud-app.md section 3.10).
import { BadRequestException, ForbiddenException } from '@nestjs/common';
import { z } from 'zod';

import { LOCALAI_PERMISSIONS } from './localai.permissions';
import { settingDef, settingPermission } from './localai.settings';

/** `{ "<dotted key>": value, ... }`: at least one key, at most the whole table. */
export const PutSettingsBodySchema = z.record(z.string().min(1).max(80), z.unknown()).refine((body) => Object.keys(body).length > 0, 'Send at least one setting');

/** The keys a caller who holds `permissions` must not see (the tailnet name is for administrators only). */
const HIDDEN_FROM_READERS = ['gateway.tailnetSuffix'] as const;

/** The flat settings a caller may read: everything, except the tailnet name unless they are a lab or gateway administrator. */
export function visibleSettings(flat: Readonly<Record<string, unknown>>, permissions: readonly string[]): Record<string, unknown> {
  const admin = permissions.includes(LOCALAI_PERMISSIONS.LAB_ADMIN.id) || permissions.includes(LOCALAI_PERMISSIONS.GATEWAY_ADMIN.id);
  const out: Record<string, unknown> = { ...flat };
  if (!admin) for (const key of HIDDEN_FROM_READERS) delete out[key];
  return out;
}

/**
 * Validates a PUT body against the key table and the caller's permissions: an unknown key is a 400, a key whose
 * permission the caller lacks is a 403 (a body mixing `gateway.*` with other keys needs both), a bad value is a 400.
 * Answers the parsed values.
 */
export function checkSettingsBody(body: unknown, permissions: readonly string[]): Record<string, unknown> {
  const parsed = PutSettingsBodySchema.safeParse(body);
  if (!parsed.success) {
    throw new BadRequestException({ message: 'The request is not valid', details: { issues: parsed.error.issues.map((issue) => ({ path: issue.path.join('.'), message: issue.message })) } });
  }
  const entries = Object.entries(parsed.data);
  const unknown = entries.map(([key]) => key).filter((key) => settingDef(key) === undefined);
  if (unknown.length > 0) {
    throw new BadRequestException({ message: `Unknown setting: ${unknown.join(', ')}`, details: { issues: unknown.map((key) => ({ path: key, message: 'Unknown setting' })) } });
  }
  const missing = [...new Set(entries.map(([key]) => settingPermission(key)).filter((permission) => !permissions.includes(permission)))];
  if (missing.length > 0) {
    throw new ForbiddenException(`Changing these settings needs ${missing.join(' and ')}`);
  }
  const issues: Array<{ path: string; message: string }> = [];
  const values: Record<string, unknown> = {};
  for (const [key, value] of entries) {
    const result = settingDef(key)!.schema.safeParse(value);
    if (result.success) values[key] = result.data;
    else for (const issue of result.error.issues) issues.push({ path: [key, ...issue.path].join('.'), message: issue.message });
  }
  if (issues.length > 0) throw new BadRequestException({ message: 'The request is not valid', details: { issues } });
  return values;
}
