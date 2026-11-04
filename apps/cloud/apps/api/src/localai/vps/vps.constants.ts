// Names of the VPS stack-agent slice (docs/specs/cloud-app.md section 4.10). All of these are PERMANENT strings: job
// types and brokered-credential kinds are stored, audit actions are filtered on.

/** The two node-eligible job types; only the VPS stack-agent lists them (`laictl node start --types …`). */
export const JOB_VPS_PUBLISH = 'localai.vps.publish_node';
export const JOB_VPS_UNPUBLISH = 'localai.vps.unpublish_node';
export const VPS_JOB_TYPES = [JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH] as const;

/** The jobs' `subjectType`; `subjectId` is the lab node id (`ai-n3`), so an active job per node is de-duplicated. */
export const VPS_JOB_SUBJECT = 'localai_node';

/** `JobSecretBroker.kind` of the Route53 credential handed to the stack-agent for one job. */
export const VPS_SECRET_KIND = 'localai.vps.route53';

/** The worker name the runbook registers (`laictl node start --name vps-stack-agent`). */
export const VPS_AGENT_WORKER_NAME = 'vps-stack-agent';

/** The reserved key of `localai_nodes.publish_checklist` that holds the last publish/unpublish journal. */
export const VPS_JOURNAL_KEY = 'vps';

/** The address of the Route53 key in the deployment credential store: `('localai_route53', 'default')`. */
export const ROUTE53_CREDENTIAL_NAME = 'default';

/** Audit actions (permanent strings, spec section 3.5). */
export const AUDIT_VPS_PUBLISH_REQUESTED = 'localai.vps.publish_requested';
export const AUDIT_VPS_UNPUBLISH_REQUESTED = 'localai.vps.unpublish_requested';
export const AUDIT_VPS_PUBLISHED = 'localai.vps.published';
export const AUDIT_VPS_UNPUBLISHED = 'localai.vps.unpublished';
export const AUDIT_VPS_PUBLISH_FAILED = 'localai.vps.publish_failed';
export const AUDIT_VPS_ROUTE53_KEY_SET = 'localai.vps.route53_key_set';
export const AUDIT_VPS_ROUTE53_KEY_CLEARED = 'localai.vps.route53_key_cleared';
export const AUDIT_VPS_TARGET = 'localai_node';

/** The audit actions of this slice, for the audit page's known-action list (CLOUD audit, when it lands). */
export const VPS_AUDIT_ACTIONS = [
  AUDIT_VPS_PUBLISH_REQUESTED,
  AUDIT_VPS_UNPUBLISH_REQUESTED,
  AUDIT_VPS_PUBLISHED,
  AUDIT_VPS_UNPUBLISHED,
  AUDIT_VPS_PUBLISH_FAILED,
  AUDIT_VPS_ROUTE53_KEY_SET,
  AUDIT_VPS_ROUTE53_KEY_CLEARED,
] as const;
