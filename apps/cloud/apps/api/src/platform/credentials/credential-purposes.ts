// The app's credential purposes: what the credential stores accept beyond the
// platform's (storage, email, notifications and AI contribute theirs). A
// purpose is the cipher's sub-key domain, so it is PERMANENT once a row exists:
// add one, never rename one. Registered by `slices/register.ts` before
// bootstrap; the stores refuse a write to an unregistered purpose.
import type { CredentialPurposeDef } from '@marinoscar/platform-api/credentials';

/**
 * The llama-server API key of one lab node (docs/specs/cloud-app.md section 3.8). Address:
 * `('localai_node_llama_key', '<node-id>')`, for example `('localai_node_llama_key', 'ai-n3')`.
 * Written only by `POST /api/nodes/{id}/localai/key`; read only server-side (`NodeKeyService.getKey`).
 */
export const LOCALAI_NODE_LLAMA_KEY_PURPOSE: CredentialPurposeDef = {
  purpose: 'localai_node_llama_key', // PERMANENT: it is the cipher's sub-key domain. No ':' allowed in a purpose.
  owner: 'localai',
  label: 'Local AI node llama-server API key',
  tiers: ['system'], // deployment scope
};

/**
 * The IAM key the VPS stack-agent uses to publish a node's DNS record (docs/specs/cloud-app.md section 4.10). The key is
 * restricted in AWS to `route53:ChangeResourceRecordSets` on the lab's hosted zone and `route53:GetChange`. Three
 * entries of this purpose make one key: `access_key_id`, `secret_access_key` and `hosted_zone_id`. Written only by
 * `PUT /api/localai/vps/route53`; read only server-side, by the per-job secret broker. Never shown after it is saved.
 */
export const LOCALAI_ROUTE53_PURPOSE: CredentialPurposeDef = {
  purpose: 'localai_route53', // PERMANENT: it is the cipher's sub-key domain.
  owner: 'localai',
  label: 'Local AI Route53 key for the VPS stack-agent',
  tiers: ['system'], // deployment scope
};

/** Every purpose this app adds. Append here. */
export const APP_CREDENTIAL_PURPOSES: readonly CredentialPurposeDef[] = [LOCALAI_NODE_LLAMA_KEY_PURPOSE, LOCALAI_ROUTE53_PURPOSE];
