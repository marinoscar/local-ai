// Request bodies and query strings of the catalog routes (docs/specs/cloud-app.md section 3.9), validated with Zod.
import { BadRequestException } from '@nestjs/common';
import { z } from 'zod';

import { isCatalogVersion, versionOfCatalogTag } from './version';

export const ImportBodySchema = z
  .object({
    tag: z
      .string()
      .max(64)
      .refine((tag) => versionOfCatalogTag(tag) !== null, 'tag must be catalog-<YYYY.MM.DD-N>'),
  })
  .strict();
export type ImportBody = z.infer<typeof ImportBodySchema>;

export const CreateVersionBodySchema = z
  .object({
    version: z.string().refine(isCatalogVersion, 'version must be YYYY.MM.DD-N'),
    baseVersion: z.string().refine(isCatalogVersion, 'baseVersion must be YYYY.MM.DD-N').optional(),
    files: z
      .array(z.object({ path: z.string().max(200), content: z.string() }).strict())
      .min(1)
      .max(64),
    notes: z.string().max(2000).optional(),
    source: z.enum(['editor', 'upload']).default('editor'),
  })
  .strict();
export type CreateVersionBody = z.infer<typeof CreateVersionBodySchema>;

export const ApproveBodySchema = z.object({ notifyNodes: z.boolean().default(false) }).strict();
export type ApproveBody = z.infer<typeof ApproveBodySchema>;

export const GetVersionQuerySchema = z.object({ diff: z.literal('approved').optional() }).strict();
export type GetVersionQuery = z.infer<typeof GetVersionQuerySchema>;

/** Parses a body or query with `schema`; a failure is a 400 in the platform's error format with the Zod issues. */
export function parseWith<T extends z.ZodType>(schema: T, value: unknown): z.infer<T> {
  const parsed = schema.safeParse(value ?? {});
  if (!parsed.success) {
    throw new BadRequestException({
      message: 'The request is not valid.',
      details: { issues: parsed.error.issues.map((issue) => ({ path: issue.path.join('.'), message: issue.message })) },
    });
  }
  return parsed.data;
}
