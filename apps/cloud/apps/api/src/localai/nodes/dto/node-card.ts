// The wire shapes of `GET /api/localai/nodes` and `GET /api/localai/nodes/:nodeId` (docs/specs/cloud-app.md section
// 3.9). `NodeCardDto` is mirrored, as plain data, by `packages/ui/src/lab/types.ts` (the shared card components take
// it as props); `node-card.contract.spec.ts` parses the shared fixture with the Zod schema below so the two cannot
// drift unnoticed. NEVER add `tailnetIp` to a card: it is admin-only and reaches a client only through the detail
// route's `?reveal=tailnet`.
import { z } from 'zod';

export const NodeCardDtoSchema = z
  .object({
    nodeId: z.string(),
    displayName: z.string(),
    hardware: z.string().nullable(),
    backend: z.enum(['cuda', 'vulkan', 'cpu']).nullable(),
    phase: z.enum(['pending', 'enrolled', 'retired']),
    mode: z.enum(['online', 'offline', 'survival']).nullable(),
    health: z.enum(['healthy', 'degraded', 'down', 'quiet']),
    routable: z.boolean(),
    lastStateAt: z.string().nullable(),
    loadedModels: z.array(z.object({ id: z.string(), isDefault: z.boolean() }).strict()),
    lastDefault: z.string().nullable(),
    vram: z.object({ usedMib: z.number(), totalMib: z.number() }).strict().nullable(),
    ark: z.array(
      z
        .object({
          rootId: z.string(),
          kind: z.enum(['internal', 'ark_drive']),
          present: z.boolean(),
          colour: z.enum(['green', 'amber', 'red', 'grey']),
          label: z.string(),
          driveAgeDays: z.number().nullable(),
        })
        .strict(),
    ),
    sync: z
      .object({
        result: z.enum(['ok', 'partial', 'failed', 'skipped']).nullable(),
        finishedAt: z.string().nullable(),
        skipReason: z.string().nullable(),
      })
      .strict(),
    drill: z.object({ result: z.enum(['pass', 'warn', 'fail']).nullable() }).strict(),
    agentVersion: z.string().nullable(),
    agentBehind: z.boolean(),
    catalogVersion: z.string().nullable(),
    catalogBehind: z.boolean(),
    alerts: z.object({ active: z.number().int(), critical: z.number().int() }).strict(),
  })
  .strict();
export type NodeCardDto = z.infer<typeof NodeCardDtoSchema>;

/** The header counts of the Lab page ("3 nodes · 2 online · 1 offline mode · 2 alerts · Gateway: 2 nodes"). */
export const LabSummaryDtoSchema = z
  .object({
    /** Nodes in the list (retired nodes are not listed). */
    nodes: z.number().int(),
    /** Mode `online` and health `healthy` or `degraded`. */
    online: z.number().int(),
    /** Mode `offline` or `survival` (expected silence). */
    offlineMode: z.number().int(),
    /** Sum of the nodes' active alerts. */
    activeAlerts: z.number().int(),
    /** Count of `routable` nodes: the "Gateway: N nodes" figure. */
    routable: z.number().int(),
  })
  .strict();
export type LabSummaryDto = z.infer<typeof LabSummaryDtoSchema>;

export const LabNodesResponseSchema = z.object({ summary: LabSummaryDtoSchema, nodes: z.array(NodeCardDtoSchema) }).strict();
export type LabNodesResponse = z.infer<typeof LabNodesResponseSchema>;

/**
 * The detail payload: the card fields plus the last snapshot. `tailnetIp` exists only when revealed. `keyReceived` is
 * whether the node's llama key has reached the cloud (never the key or its hint): the Add-node wizard polls it.
 */
export type NodeDetailDto = NodeCardDto & { lastState: unknown; keyReceived: boolean; tailnetIp?: string | null };
