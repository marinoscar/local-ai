// The `GET /api/nodes/{id}/localai/config` handshake (docs/specs/cloud-app.md section 3.6).
//
// The Go agent mirrors this in `agent/internal/cloud` (agent.md section 4.8.2) and both suites load the same
// fixture, `test/fixtures/localai/config.response.json`. Field names are a permanent wire contract: an agent
// that is months old (a laptop that was offline) must still understand the answer.
import { z } from 'zod';

/** The version of the node-facing Local AI HTTP contract this cloud speaks. */
export const LOCALAI_NODE_API_VERSION = 1;

/** The app routes under `/api/nodes/{id}/localai/` the agent may use. A feature is listed when its route exists. */
export const LOCALAI_NODE_FEATURES = ['state.v1', 'commands.v1', 'catalog.v1', 'key.v1'] as const;

/**
 * Which command plane the agent claims from. `localai` (the app-owned `localai_commands` table) until EAB ships
 * targeted jobs (seam requests SR-1 and SR-2, spec section 3.12), then `eab`.
 */
export const LOCALAI_COMMAND_PLANES = ['localai', 'eab'] as const;
export type LocalaiCommandPlane = (typeof LOCALAI_COMMAND_PLANES)[number];

/** The values the handshake tells every node. Constants for now; the agent clamps nothing, so keep them sane. */
export const LOCALAI_NODE_LINK_TIMING = {
  heartbeatIntervalS: 15,
  stateMinIntervalS: 5,
  stateMaxIntervalS: 60,
  claimWaitMaxMs: 25_000,
} as const;

export const LocalaiConfigResponseSchema = z
  .object({
    nodeId: z.string().min(1),
    apiVersion: z.literal(LOCALAI_NODE_API_VERSION),
    features: z.array(z.enum(LOCALAI_NODE_FEATURES)),
    commandPlane: z.enum(LOCALAI_COMMAND_PLANES),
    heartbeatIntervalS: z.number().int().positive(),
    stateMinIntervalS: z.number().int().positive(),
    stateMaxIntervalS: z.number().int().positive(),
    claimWaitMaxMs: z.number().int().positive(),
    keyReported: z.boolean(),
    serverTime: z.string().datetime(),
  })
  .strict();

export type LocalaiConfigResponse = z.infer<typeof LocalaiConfigResponseSchema>;
