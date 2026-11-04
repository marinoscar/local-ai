// The refusals of the binding guard (docs/specs/cloud-app.md section 4.2) in EAB's error format: the HTTP status
// and message are EAB's, and our doctor code and a machine-readable reason travel in `details`, which is where the
// agent reads them (`details.code`, agent.md section 4.8.2).
//
// A message is plain language for an operator and NEVER carries credential material, the claimed node's data or
// anything about the bound credential beyond the fact that it does not match.
import { ForbiddenException, NotFoundException } from '@nestjs/common';

/** The doctor codes the guard answers (docs/specs/doctor-codes.md, area CLD). */
export const LAI_CLD_WRONG_NODE = 'LAI-CLD-004';
export const LAI_CLD_NOT_ENROLLED = 'LAI-CLD-035';

/** Every `details.reason` the guard can give. Permanent strings: the agent and the cloud UI match on them. */
export type BindingRefusalReason =
  | 'node_credential_required'
  | 'unknown_localai_node'
  | 'credential_bound_to_other_node'
  | 'node_retired'
  | 'credential_not_bound';

export interface BindingRefusalDetails {
  code: typeof LAI_CLD_WRONG_NODE | typeof LAI_CLD_NOT_ENROLLED;
  reason: BindingRefusalReason;
}

const MESSAGES: Record<BindingRefusalReason, string> = {
  node_credential_required: 'Only a node credential can call the Local AI node routes',
  unknown_localai_node: 'No Local AI node has this id',
  credential_bound_to_other_node: 'This credential belongs to a different node',
  node_retired: 'This node is retired',
  credential_not_bound: 'This node has no credential bound to it yet',
};

/** Builds the exception for a refusal: 403 for everything except an unknown Local AI node (404). */
export function bindingRefusal(code: BindingRefusalDetails['code'], reason: BindingRefusalReason): ForbiddenException | NotFoundException {
  const body = { message: MESSAGES[reason], details: { code, reason } satisfies BindingRefusalDetails };
  return reason === 'unknown_localai_node' ? new NotFoundException(body) : new ForbiddenException(body);
}
