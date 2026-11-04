// The journal and results of the VPS stack-agent (docs/specs/cloud-app.md section 4.10). The API validates what the
// node returns with the strict Zod schema in apps/api/src/localai/vps/vps-result.schema.ts; these are its shapes.

export const VPS_PUBLISH_TYPE = 'localai.vps.publish_node';
export const VPS_UNPUBLISH_TYPE = 'localai.vps.unpublish_node';

/** The kind of the brokered credential (`JobSecretBroker.kind`; permanent). */
export const VPS_SECRET_KIND = 'localai.vps.route53';

export type PublishStepName = 'dns_upsert' | 'dns_wait' | 'bootstrap_vhost' | 'certificate' | 'final_vhost' | 'validate';
export type UnpublishStepName = 'vhost_remove' | 'dns_delete' | 'certificate_kept';
export type StepStatus = 'done' | 'skipped' | 'failed';

export interface JournalStep<Name extends string = string> {
  name: Name;
  status: StepStatus;
  /** A fixed sentence, scrubbed, at most 500 characters. */
  detail: string;
}

export interface PublishResult {
  host: string;
  steps: Array<JournalStep<PublishStepName>>;
  certificateExpiresAt: string | null;
}

export interface UnpublishResult {
  host: string;
  steps: Array<JournalStep<UnpublishStepName>>;
}

/** What the API's secret broker hands the executor for one job (never in the params). */
export interface VpsSecret {
  route53AccessKeyId: string;
  route53SecretAccessKey: string;
  hostedZoneId: string;
  host: string;
  stagingSuffix: string;
  vpsPublicHostname: string;
  /** Publish only. */
  servePattern?: 'A' | 'B';
  tailnetIp?: string;
  tailnetMachine?: string;
}
