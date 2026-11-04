// Builds the `GET .../localai/config` answer (docs/specs/cloud-app.md section 3.6) from the verified node row.
import type { LocalaiNode } from '@prisma/client';

import {
  LOCALAI_NODE_API_VERSION,
  LOCALAI_NODE_FEATURES,
  LOCALAI_NODE_LINK_TIMING,
  LocalaiConfigResponseSchema,
  type LocalaiCommandPlane,
  type LocalaiConfigResponse,
} from '../contracts/config.schema';

/** `commandPlane` is `localai` until EAB ships targeted jobs (seam requests SR-1 and SR-2), then `eab` (CLOUD-20). */
export const LOCALAI_COMMAND_PLANE: LocalaiCommandPlane = 'localai';

export function buildLocalaiConfig(node: Pick<LocalaiNode, 'nodeId' | 'keyReportedAt'>, now: Date): LocalaiConfigResponse {
  // Parsed through the strict schema so the wire shape can never drift from the contract the agent mirrors.
  return LocalaiConfigResponseSchema.parse({
    nodeId: node.nodeId,
    apiVersion: LOCALAI_NODE_API_VERSION,
    features: [...LOCALAI_NODE_FEATURES],
    commandPlane: LOCALAI_COMMAND_PLANE,
    ...LOCALAI_NODE_LINK_TIMING,
    keyReported: node.keyReportedAt !== null,
    serverTime: now.toISOString(),
  });
}
