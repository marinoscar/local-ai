// The bodies of the node-facing command routes (docs/specs/cloud-app.md section 3.6). Their field names mirror EAB's
// worker-node `claim` / `renew` / `result` / `failure` so the agent's EAB client is reused with another path prefix
// (agent.md section 4.8.6): `jobs`, `job`, `claimToken`, `renewIntervalMs`, `leaseExpiresAt`, `traceparent`. The
// shared examples are `test/fixtures/localai/claim.*.json`, `renew.*.json`, `result.*.json` and `failure.*.json`;
// the agent's fake cloud loads the same files. The request schemas are strict except where the spec says a field is
// accepted and ignored (`rateLimited`, `retryAfterMs`).
import { z } from 'zod';

/** `POST …/commands/claim` body. `types` are names WITHOUT the `localai.` prefix; unknown ones are dropped later. */
export const ClaimRequestSchema = z
  .object({
    types: z.array(z.string().min(1).max(64)).max(64),
    // Both are CLAMPED by the route (limit to 1, waitMs to [0, claimWaitMaxMs]), never refused.
    limit: z.number().optional(),
    waitMs: z.number().optional(),
  })
  .strict();
export type ClaimRequest = z.infer<typeof ClaimRequestSchema>;

/** `POST …/commands/{id}/renew` body. */
export const RenewRequestSchema = z.object({ claimToken: z.string().min(1).max(64) }).strict();
export type RenewRequest = z.infer<typeof RenewRequestSchema>;

/** `POST …/commands/{id}/result` body; `result` is checked against the type's own schema afterwards. */
export const ResultRequestSchema = z
  .object({
    type: z.string().min(1).max(64),
    claimToken: z.string().min(1).max(64),
    result: z.unknown(),
  })
  .strict()
  .refine((body) => body.result !== undefined, { message: 'result is required', path: ['result'] });
export type ResultRequest = z.infer<typeof ResultRequestSchema>;

/** `POST …/commands/{id}/failure` body (EAB's `nodeJobFailureSchema`: `rateLimited` and `retryAfterMs` are ignored). */
export const FailureRequestSchema = z
  .object({
    claimToken: z.string().min(1).max(64),
    error: z.string().min(1).max(2000),
    rateLimited: z.boolean().optional(),
    retryAfterMs: z.number().optional(),
  })
  .strict();
export type FailureRequest = z.infer<typeof FailureRequestSchema>;

/** One assignment in the claim answer. */
export const ClaimedJobSchema = z
  .object({
    job: z
      .object({
        id: z.string().uuid(),
        type: z.string(),
        attempts: z.number().int().min(1),
        createdAt: z.string().datetime(),
      })
      .strict(),
    params: z.record(z.string(), z.unknown()),
    claimToken: z.string().uuid(),
    renewIntervalMs: z.number().int().positive(),
    leaseExpiresAt: z.string().datetime(),
    traceparent: z.string().nullable(),
  })
  .strict();

/** The claim answer's `data`. An empty `jobs` array is the normal answer. */
export const ClaimResponseSchema = z.object({ jobs: z.array(ClaimedJobSchema) }).strict();
export type ClaimedJob = z.infer<typeof ClaimedJobSchema>;
export type ClaimResponse = z.infer<typeof ClaimResponseSchema>;

/** The renew answer's `data`. */
export const RenewResponseSchema = z.object({ leaseExpiresAt: z.string().datetime() }).strict();
export type RenewResponse = z.infer<typeof RenewResponseSchema>;

/** The result and failure answers' `data`. */
export const SettleResponseSchema = z.object({ status: z.enum(['succeeded', 'failed']) }).strict();
export type SettleResponse = z.infer<typeof SettleResponseSchema>;
