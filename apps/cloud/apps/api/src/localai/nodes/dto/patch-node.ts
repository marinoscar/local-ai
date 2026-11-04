// The body of `PATCH /api/localai/nodes/:nodeId` (docs/specs/cloud-app.md section 3.9, lab:admin): any of the node's
// editable fields, strictly validated. An unknown key is a 400, an empty body is a 400, and a blank nullable text is
// stored as null. The tailnet address is validated for SHAPE only and is never echoed back or logged.
import { isIP } from 'node:net';
import { z } from 'zod';

const HOSTNAME = /^(?=.{1,253}$)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/;
const MACHINE = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const CATALOG_VERSION = /^\d{4}\.\d{2}\.\d{2}-\d+$/;

/** Blank (or whitespace) text means "clear it". */
const blankToNull = (value: unknown) => (typeof value === 'string' && value.trim() === '' ? null : value);

/** True for an address in Tailscale's ranges: IPv4 in the shared (CGNAT) block, second octet 64 to 127, or the Tailscale IPv6 prefix. */
export function isTailnetAddress(value: string): boolean {
  const family = isIP(value);
  if (family === 4) {
    const [first, second] = value.split('.').map(Number) as [number, number];
    return first === 100 && second >= 64 && second <= 127;
  }
  if (family === 6) return value.toLowerCase().startsWith('fd7a:115c:a1e0:');
  return false;
}

export const PatchNodeBodySchema = z
  .object({
    displayName: z.string().trim().min(1).max(80).optional(),
    publicHost: z.string().trim().toLowerCase().regex(HOSTNAME, 'Enter a host name such as ai-n2.example.test').optional(),
    servePattern: z.enum(['A', 'B', 'none']).optional(),
    tailnetMachine: z.preprocess(blankToNull, z.string().trim().toLowerCase().regex(MACHINE, 'Enter the machine name, letters, digits and hyphens').nullable()).optional(),
    tailnetIp: z.preprocess(blankToNull, z.string().trim().refine(isTailnetAddress, 'Enter a tailnet address').nullable()).optional(),
    gatewayEnabled: z.boolean().optional(),
    gatewayPriority: z.number().int().min(0).max(10_000).optional(),
    catalogPin: z.preprocess(blankToNull, z.string().trim().regex(CATALOG_VERSION, 'Enter a catalog version such as 2026.11.02-1').nullable()).optional(),
  })
  .strict()
  .refine((body) => Object.values(body).some((value) => value !== undefined), { message: 'Send at least one field to change' });
export type PatchNodeBody = z.infer<typeof PatchNodeBodySchema>;

/** The body of `POST /api/localai/nodes/:nodeId/retire`: the node id typed by the operator. */
export const RetireNodeBodySchema = z.object({ confirm: z.string() }).strict();
export type RetireNodeBody = z.infer<typeof RetireNodeBodySchema>;
