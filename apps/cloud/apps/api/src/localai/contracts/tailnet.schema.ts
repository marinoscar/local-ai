// The `POST /api/nodes/{id}/localai/tailnet` wire contract (docs/specs/cloud-app.md section 3.6): the node tells the
// cloud its own Tailscale address and name, so the Add-node wizard no longer asks a person to type them (issue #437).
//
// The Go agent mirrors it in `agent/internal/cloud/tailnet.go` (`tailnetBody`). Field names are a permanent wire
// contract. HANDLING: the address is admin-only data. Nothing here puts a received value into a message (a failed parse
// is reduced to the field names), the answer never echoes it, and the audit row says `<set>`, never the value.
import { z } from 'zod';

import { isTailnetAddress } from '../wizard/wizard.schemas';

const MACHINE = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?){0,4}$/;

export const NodeTailnetRequestSchema = z
  .object({
    ip: z.string().trim().refine(isTailnetAddress, 'not a Tailscale address'),
    /** The machine name (`dell-linux`) or its full MagicDNS name (`dell-linux.<tailnet>.ts.net`), without a trailing dot. */
    machine: z.string().trim().toLowerCase().regex(MACHINE, 'not a machine name').optional(),
  })
  .strict();

export type NodeTailnetRequest = z.infer<typeof NodeTailnetRequestSchema>;

/** `200 { data }`: what was done, never the values. */
export const NodeTailnetResponseSchema = z.object({ stored: z.boolean() }).strict();
export type NodeTailnetResponse = z.infer<typeof NodeTailnetResponseSchema>;

/** The names of the fields that failed, for a 400; never a value. */
export function tailnetIssueFields(error: z.ZodError): string[] {
  return [...new Set(error.issues.map((issue) => issue.path.map(String).join('.') || '(body)'))];
}
