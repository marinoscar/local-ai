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

/** What the Settings tab shows. The tailnet address is a flag here: the value only ever comes with `?reveal=tailnet`. */
export interface NodeSettingsDto {
  publicHost: string;
  servePattern: 'A' | 'B' | 'none';
  tailnetMachine: string | null;
  tailnetIpSet: boolean;
  gatewayEnabled: boolean;
  gatewayPriority: number;
  catalogPin: string | null;
  profile: string | null;
  connectivity: string | null;
}

/** What the Keys tab shows: a hint and prefixes, NEVER key or token material. */
export interface NodeKeysDto {
  /** `ab12…9z0f` (first and last four of the llama key), or null before the node reported one. */
  keyHint: string | null;
  keyReportedAt: string | null;
  /** The bound `nod_` credential's display fragment and last use; null when none is bound. */
  credential: { prefix: string; lastUsedAt: string | null; createdAt: string } | null;
}

/**
 * The detail payload: the card fields plus the last snapshot and what the six tabs need. `tailnetIp` exists only when
 * revealed. `acceptedTypes` is the command types the node allows (its reported allow list, section 3.6 `register`),
 * `null` when the cloud does not know (no linked worker row); `modelSizes` maps model id to planned bytes from the
 * approved catalog (best effort); `agentLatestVersion` is the `localai` setting `agent.latestVersion`.
 */
export type NodeDetailDto = NodeCardDto & {
  lastState: unknown;
  /** Whether the node's llama key has reached the cloud (never the key or its hint): the Add-node wizard polls it. */
  keyReceived: boolean;
  tailnetIp?: string | null;
  settings: NodeSettingsDto;
  keys: NodeKeysDto;
  acceptedTypes: string[] | null;
  modelSizes: Record<string, number>;
  agentLatestVersion: string | null;
};
