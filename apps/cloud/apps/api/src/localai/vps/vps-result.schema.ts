// What the stack-agent posts back (docs/specs/cloud-app.md section 4.10). The value arrives from a machine this server
// does not control, so these STRICT schemas are the trust boundary in front of `persistNodeResult`.
//
// Differences from the issue's text (recorded in the spec, "As built"): the host regex admits the staging suffix
// (otherwise a staging publish could never be accepted), and unpublish has its own step names.
import { z } from 'zod';

import { VPS_HOST_RE } from './vps-host';

export const PUBLISH_STEP_NAMES = ['dns_upsert', 'dns_wait', 'bootstrap_vhost', 'certificate', 'final_vhost', 'validate'] as const;
export const UNPUBLISH_STEP_NAMES = ['vhost_remove', 'dns_delete', 'certificate_kept'] as const;
export type PublishStepName = (typeof PUBLISH_STEP_NAMES)[number];
export type UnpublishStepName = (typeof UNPUBLISH_STEP_NAMES)[number];

const status = z.enum(['done', 'skipped', 'failed']);
const host = z.string().regex(VPS_HOST_RE);
const detail = z.string().max(500);

export const PublishResultSchema = z
  .object({
    host,
    steps: z.array(z.object({ name: z.enum(PUBLISH_STEP_NAMES), status, detail }).strict()).max(10),
    certificateExpiresAt: z.string().datetime({ offset: true }).nullable(),
  })
  .strict();
export type PublishResult = z.infer<typeof PublishResultSchema>;

export const UnpublishResultSchema = z
  .object({
    host,
    steps: z.array(z.object({ name: z.enum(UNPUBLISH_STEP_NAMES), status, detail }).strict()).max(10),
  })
  .strict();
export type UnpublishResult = z.infer<typeof UnpublishResultSchema>;

/** The journal as kept in `publish_checklist.vps` and served to the wizard. */
export interface StoredJournal {
  jobId: string;
  action: 'publish' | 'unpublish';
  host: string;
  outcome: 'done' | 'failed';
  finishedAt: string;
  steps: Array<{ name: string; status: 'done' | 'skipped' | 'failed'; detail: string }>;
  certificateExpiresAt: string | null;
}
