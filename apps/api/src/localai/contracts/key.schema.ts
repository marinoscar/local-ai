// The `POST /api/nodes/{id}/localai/key` wire contract (docs/specs/cloud-app.md section 3.6).
//
// The Go agent mirrors it in `agent/internal/cloud/key.go` (`keyBody`) and its fake cloud
// (`agent/internal/cloud/fake/server.go`); both suites load the same fixture, `test/fixtures/localai/key.request.json`.
// Field names are a permanent wire contract.
//
// SECRET HANDLING. `key` is the llama-server API key. Nothing in this file may put a received value into a message:
// a Zod issue is reduced to its path and code ({@link KeyIssue}), because Zod's default messages and some issue
// fields (`received`, `input`) echo the value.
import { z } from 'zod';

const KEY_HEX = /^[0-9a-f]{64}$/;
const FINGERPRINT = /^[0-9a-f]{4}…[0-9a-f]{4}$/;

/** `first4…last4` of a key: the only part of it the cloud ever displays or logs (spec section 3.8). */
export function fingerprintOf(key: string): string {
  return `${key.slice(0, 4)}…${key.slice(-4)}`;
}

export const NodeKeyRequestSchema = z
  .object({
    key: z.string().regex(KEY_HEX),
    fingerprint: z.string().regex(FINGERPRINT),
    rotated_at: z.string().datetime({ offset: true }),
  })
  .strict();

export type NodeKeyRequest = z.infer<typeof NodeKeyRequestSchema>;

/** `200 { data }` of the key route. `keyHint` is the fingerprint; `storedAt` is the server's clock. */
export const NodeKeyResponseSchema = z
  .object({
    keyHint: z.string().regex(FINGERPRINT),
    storedAt: z.string().datetime(),
  })
  .strict();

export type NodeKeyResponse = z.infer<typeof NodeKeyResponseSchema>;

/** One validation problem, safe to return: where and what kind, never the value. */
export interface KeyIssue {
  path: string[];
  code: string;
}

/** Why a request body was refused (the codes are permanent strings the agent may log). */
export type KeyRefusal = { kind: 'invalid'; issues: KeyIssue[] } | { kind: 'fingerprint_mismatch'; issues: KeyIssue[] };

/**
 * Validates a request body: the strict schema, then the fingerprint recomputed from the key. Returns the typed body or
 * the refusal. Never throws and never includes a received value in the result.
 */
export function parseNodeKeyRequest(body: unknown): { ok: true; value: NodeKeyRequest } | { ok: false; refusal: KeyRefusal } {
  const parsed = NodeKeyRequestSchema.safeParse(body);
  if (!parsed.success) {
    const issues = parsed.error.issues.map((issue) => ({ path: issue.path.map(String), code: issue.code }));
    return { ok: false, refusal: { kind: 'invalid', issues } };
  }
  if (fingerprintOf(parsed.data.key) !== parsed.data.fingerprint) {
    return { ok: false, refusal: { kind: 'fingerprint_mismatch', issues: [{ path: ['fingerprint'], code: 'fingerprint_mismatch' }] } };
  }
  return { ok: true, value: parsed.data };
}
