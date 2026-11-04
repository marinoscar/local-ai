import { KNOWN_AUDIT_ACTIONS } from '../audit/audit-actions';
import {
  AUDIT_GATEWAY_KEY_CREATED,
  AUDIT_GATEWAY_KEY_REVOKED,
  AUDIT_GATEWAY_KEY_UPDATED,
  AUDIT_GATEWAY_POLICY_UPDATED,
  AUDIT_TARGET_GATEWAY_KEY,
  AUDIT_TARGET_MODEL_POLICY,
  GATEWAY_AUDIT_ACTIONS,
} from './gateway.audit';

describe('the gateway audit actions', () => {
  // The strings are permanent: audit_events rows are stored under them (gateway.md section 3.6).
  it('are the exact permanent strings', () => {
    expect(AUDIT_GATEWAY_KEY_CREATED).toBe('localai.gateway.key_created');
    expect(AUDIT_GATEWAY_KEY_REVOKED).toBe('localai.gateway.key_revoked');
    expect(AUDIT_GATEWAY_KEY_UPDATED).toBe('localai.gateway.key_updated');
    expect(AUDIT_GATEWAY_POLICY_UPDATED).toBe('localai.gateway.policy_updated');
  });

  it('are listed once each, in filter order', () => {
    expect([...GATEWAY_AUDIT_ACTIONS]).toEqual([
      'localai.gateway.key_created',
      'localai.gateway.key_revoked',
      'localai.gateway.key_updated',
      'localai.gateway.policy_updated',
    ]);
  });

  it('are all offered by the audit page filter, and it offers no other gateway action', () => {
    const offered = KNOWN_AUDIT_ACTIONS.map((def) => def.action).filter((action) => action.startsWith('localai.gateway.'));
    expect(offered).toEqual([...GATEWAY_AUDIT_ACTIONS]);
  });

  it('name the target types the audit summary already links to', () => {
    expect(AUDIT_TARGET_GATEWAY_KEY).toBe('localai_gateway_key');
    expect(AUDIT_TARGET_MODEL_POLICY).toBe('localai_model_policy');
  });
});
