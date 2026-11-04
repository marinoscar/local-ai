// Request bodies, the query and the response shape of the key routes (docs/specs/gateway.md section 3.8), in Zod.
// The response never carries the token hash; the raw token appears only in the creation response (`CreatedKeyDto`).
import { BadRequestException } from '@nestjs/common';
import { z } from 'zod';

import type { KeyRecord } from './key.store';

const int = (min: number, max: number) => z.number().int().min(min).max(max);

/** A catalog id or an alias. Permissive on shape (the allowed set is checked against the catalog), strict on characters. */
export const ModelNameSchema = z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/, 'must be a model id or alias');

/** A key's name: shown in tables, audit rows and notifications, so no control characters and no long text. */
export const KeyNameSchema = z
  .string()
  .trim()
  .min(1, 'Give the key a name')
  .max(80, 'At most 80 characters')
  // eslint-disable-next-line no-control-regex
  .refine((value) => !/[\u0000-\u001f\u007f]/.test(value), 'No control characters');

export const LimitsSchema = z
  .object({
    rpm: int(1, 100_000).optional(),
    concurrent: int(1, 1_000).optional(),
    /** Tokens per UTC day. `null` = unlimited, which only a service key may have. */
    dailyTokens: int(1, Number.MAX_SAFE_INTEGER).nullable().optional(),
  })
  .strict();
export type LimitsInput = z.infer<typeof LimitsSchema>;

export const CreateKeyBodySchema = z
  .object({
    name: KeyNameSchema,
    allowedModels: z.array(ModelNameSchema).max(200).optional(),
    /** Days until the key expires. `null` = never, which only a service key may have. */
    expiresInDays: int(1, 3_650).nullable().optional(),
    kind: z.enum(['user', 'service']).optional(),
    ownerUserId: z.string().uuid().optional(),
    limits: LimitsSchema.optional(),
  })
  .strict();
export type CreateKeyBody = z.infer<typeof CreateKeyBodySchema>;

export const PatchKeyBodySchema = z
  .object({
    name: KeyNameSchema.optional(),
    allowedModels: z.array(ModelNameSchema).max(200).optional(),
    limits: LimitsSchema.optional(),
  })
  .strict()
  .refine((body) => body.name !== undefined || body.allowedModels !== undefined || (body.limits !== undefined && Object.keys(body.limits).length > 0), 'Send at least one change');
export type PatchKeyBody = z.infer<typeof PatchKeyBodySchema>;

const flag = z
  .enum(['1', '0', 'true', 'false'])
  .optional()
  .transform((value) => value === '1' || value === 'true');

export const ListKeysQuerySchema = z.object({ all: flag, includeRevoked: flag }).strict();
export type ListKeysQuery = z.infer<typeof ListKeysQuerySchema>;

export const KeyIdSchema = z.string().uuid();

/** Parses with `schema`; a failure is a 400 in the platform's error format with the Zod issues. */
export function parseWith<T extends z.ZodType>(schema: T, value: unknown): z.infer<T> {
  const parsed = schema.safeParse(value ?? {});
  if (!parsed.success) {
    throw new BadRequestException({ message: 'The request is not valid', details: { issues: parsed.error.issues.map((issue) => ({ path: issue.path.join('.'), message: issue.message })) } });
  }
  return parsed.data;
}

export type KeyStatus = 'active' | 'expired' | 'revoked';

/** One key as the API returns it: the display prefix, never the token or its hash. */
export interface KeyDto {
  id: string;
  name: string;
  kind: 'user' | 'service';
  /** `lai_` plus 6 characters. */
  tokenPrefix: string;
  /** Catalog ids or aliases; empty = every enabled model. */
  allowedModels: string[];
  limits: { rpm: number; concurrent: number; dailyTokens: number | null };
  expiresAt: string | null;
  revokedAt: string | null;
  lastUsedAt: string | null;
  createdAt: string;
  status: KeyStatus;
  ownerUserId: string;
  /** The owner's e-mail and name: present for a gateway administrator only. */
  owner?: { email: string; displayName: string | null };
}

export interface CreatedKeyDto {
  key: KeyDto;
  /** The raw token. Shown once, here, and never stored or returned again. */
  token: string;
}

export function keyStatus(record: Pick<KeyRecord, 'expiresAt' | 'revokedAt'>, now: Date): KeyStatus {
  if (record.revokedAt !== null) return 'revoked';
  if (record.expiresAt !== null && record.expiresAt.getTime() <= now.getTime()) return 'expired';
  return 'active';
}

/** The DTO of a row. `withOwner` adds the owner's e-mail and name (administrators only). */
export function toKeyDto(record: KeyRecord, now: Date, withOwner: boolean): KeyDto {
  const dto: KeyDto = {
    id: record.id,
    name: record.name,
    kind: record.kind,
    tokenPrefix: record.tokenPrefix,
    allowedModels: [...record.allowedModels],
    limits: { rpm: record.rpmLimit, concurrent: record.concurrentLimit, dailyTokens: record.dailyTokenBudget === null ? null : Number(record.dailyTokenBudget) },
    expiresAt: record.expiresAt ? record.expiresAt.toISOString() : null,
    revokedAt: record.revokedAt ? record.revokedAt.toISOString() : null,
    lastUsedAt: record.lastUsedAt ? record.lastUsedAt.toISOString() : null,
    createdAt: record.createdAt.toISOString(),
    status: keyStatus(record, now),
    ownerUserId: record.userId,
  };
  if (withOwner && record.user) dto.owner = { email: record.user.email, displayName: record.user.displayName };
  return dto;
}
