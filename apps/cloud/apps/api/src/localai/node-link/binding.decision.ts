// The pure part of the binding guard (docs/specs/cloud-app.md section 4.2, steps 4 to 6): given the credential that
// authenticated the request, the worker node it addresses and the Local AI node row, decide whether the call is
// admitted and what the guard must write. No I/O, so every branch is a table row in `binding.guard.spec.ts`.
//
// FAIL CLOSED: the only way to be admitted is to fall through every refusal below. A row in a phase this code does
// not know, a missing row and an unbound credential are all refusals.
import type { LocalaiNodePhase } from '@prisma/client';

import { LAI_CLD_NOT_ENROLLED, LAI_CLD_WRONG_NODE, type BindingRefusalDetails } from './node-link.errors';

/** What the decision reads from the `localai_nodes` row. */
export interface BindingNodeView {
  phase: LocalaiNodePhase;
  nodeCredentialId: string | null;
  workerNodeId: string | null;
}

export interface BindingInput {
  /** The `node_credentials.id` found from the bearer's digest. */
  credentialId: string;
  /** The worker node (`worker_nodes.id`) in the URL, already ownership-checked. */
  workerNodeId: string;
  /** The Local AI node row, or null when the worker node's name is not a known node id. */
  node: BindingNodeView | null;
}

export type BindingDecision =
  | {
      outcome: 'refuse';
      status: 403 | 404;
      details: BindingRefusalDetails;
      /** Whether the refusal is an attempt to use a credential on a node it is not bound to (audited). */
      audit: boolean;
    }
  | {
      outcome: 'admit';
      /** The first bound call: `pending` becomes `enrolled`. */
      enroll: boolean;
      /** The worker node id on the row is not the one in the URL (the node registered again): record the new one. */
      relink: boolean;
    };

export function decideBinding(input: BindingInput): BindingDecision {
  const { node } = input;
  if (node === null) {
    return { outcome: 'refuse', status: 404, details: { code: LAI_CLD_WRONG_NODE, reason: 'unknown_localai_node' }, audit: false };
  }
  if (node.phase === 'retired') {
    return { outcome: 'refuse', status: 403, details: { code: LAI_CLD_NOT_ENROLLED, reason: 'node_retired' }, audit: false };
  }
  if (node.phase !== 'pending' && node.phase !== 'enrolled') {
    // A phase added later that this code was not taught about: never admit it.
    return { outcome: 'refuse', status: 403, details: { code: LAI_CLD_NOT_ENROLLED, reason: 'node_retired' }, audit: false };
  }
  if (node.nodeCredentialId === null) {
    return { outcome: 'refuse', status: 403, details: { code: LAI_CLD_NOT_ENROLLED, reason: 'credential_not_bound' }, audit: true };
  }
  if (node.nodeCredentialId !== input.credentialId) {
    return { outcome: 'refuse', status: 403, details: { code: LAI_CLD_WRONG_NODE, reason: 'credential_bound_to_other_node' }, audit: true };
  }
  return { outcome: 'admit', enroll: node.phase === 'pending', relink: node.workerNodeId !== input.workerNodeId };
}
