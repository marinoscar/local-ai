// Request bodies of the Add-node wizard routes (docs/specs/cloud-app.md sections 3.9 and 4.8), validated with Zod.
// Messages are fixed text: they never echo the value that failed (a tailnet address is admin-only data).
import { z } from 'zod';

import { NODE_ID_RE, PUBLIC_HOST_RE } from './node-id';

/** The steps of the publish checklist, in the order the page lists them. The ids are permanent (stored in `publish_checklist`). */
export const CHECKLIST_STEPS = ['tailnet', 'dns', 'bootstrap_vhost', 'certificate', 'https_vhost', 'enrolled', 'key', 'gateway_test'] as const;
export type ChecklistStep = (typeof CHECKLIST_STEPS)[number];

export function isChecklistStep(value: string): value is ChecklistStep {
  return (CHECKLIST_STEPS as readonly string[]).includes(value);
}

/** A Tailscale address: IPv4 in the CGNAT block (first octet 100, second 64 to 127) or IPv6 in the fd7a:115c:a1e0 prefix. */
export function isTailnetAddress(value: string): boolean {
  const v4 = /^100\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(value);
  if (v4) {
    const [second, third, fourth] = [Number(v4[1]), Number(v4[2]), Number(v4[3])];
    return second >= 64 && second <= 127 && third <= 255 && fourth <= 255;
  }
  return /^fd7a:115c:a1e0(?::[0-9a-f]{1,4}){1,5}$/i.test(value);
}

const optionalText = <T extends z.ZodType>(schema: T) => z.preprocess((value) => (typeof value === 'string' && value.trim() === '' ? undefined : value), schema.optional());

export const CreateNodeBodySchema = z
  .object({
    nodeId: z.string().regex(NODE_ID_RE, 'nodeId must look like ai-n4 (ai-n followed by 1 to 9999)'),
    displayName: optionalText(z.string().trim().min(1).max(80)),
    publicHost: optionalText(z.string().trim().toLowerCase().regex(PUBLIC_HOST_RE, 'publicHost must be a DNS name such as ai-n4.marin.cr')),
    servePattern: z.enum(['A', 'B']).default('B'),
    // The machine name (`dell-linux`) or its full MagicDNS name (`dell-linux.<tailnet>.ts.net`).
    tailnetMachine: optionalText(z.string().trim().toLowerCase().regex(/^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?){0,4}$/, 'tailnetMachine must be a machine name or MagicDNS name')),
    tailnetIp: optionalText(z.string().trim().refine(isTailnetAddress, 'tailnetIp must be a Tailscale address (CGNAT range, second octet 64 to 127)')),
    profile: optionalText(z.string().trim().regex(/^[a-z0-9][a-z0-9.-]{0,39}$/, 'profile must be a catalog profile id')),
  })
  .strict();
export type CreateNodeBody = z.infer<typeof CreateNodeBodySchema>;

export const ChecklistActionBodySchema = z.object({ action: z.enum(['mark_done', 'mark_todo', 'check']) }).strict();
export type ChecklistActionBody = z.infer<typeof ChecklistActionBodySchema>;
