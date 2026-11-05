// The body of `PUT /api/localai/gateway/models/:modelId` and the shapes of the model routes (docs/specs/gateway.md
// sections 3.6 and 3.8), in Zod. The rules that need other rows (an alias equal to a catalog id, an alias another model
// owns) are the service's; everything a body can say about itself is checked here.
import { BadRequestException } from '@nestjs/common';
import { z } from 'zod';

import { ModelId, NodeIdStr } from '../../contracts/state.schema';

/** Aliases and catalog ids share one shape (`lab-coder`, `qwen3.6-35b-a3b-q4km`). */
export const AliasSchema = ModelId;

export const MAX_ALIASES = 16;
export const MAX_PREFERRED_NODES = 32;

const unique = (values: readonly string[]): boolean => new Set(values).size === values.length;

export const PutPolicyBodySchema = z
  .object({
    enabled: z.boolean(),
    aliases: z.array(AliasSchema).max(MAX_ALIASES).refine(unique, 'An alias is listed twice'),
    onDemandLoad: z.boolean(),
    /** Seconds a request waits for an on-demand load. */
    loadWaitS: z.number().int().min(10).max(600),
    /** Overrides `gateway.timeouts.firstByteMs` for this model; null = the setting. */
    firstByteTimeoutS: z.number().int().min(1).max(3600).nullable(),
    /** Node ids in order of preference. */
    preferredNodes: z.array(NodeIdStr).max(MAX_PREFERRED_NODES).refine(unique, 'A node is listed twice'),
  })
  .strict();
export type PutPolicyBody = z.infer<typeof PutPolicyBodySchema>;

export const ModelIdParamSchema = ModelId;

/** Parses with `schema`; a failure is a 400 in the platform's error format with the Zod issues. */
export function parseWith<T extends z.ZodType>(schema: T, value: unknown): z.infer<T> {
  const parsed = schema.safeParse(value ?? {});
  if (!parsed.success) {
    throw new BadRequestException({ message: 'The request is not valid', details: { issues: parsed.error.issues.map((issue) => ({ path: issue.path.join('.'), message: issue.message })) } });
  }
  return parsed.data;
}

/** What a model without a policy row gets: the policy of the row is {@link ModelPolicyDto}. */
export interface ModelPolicyDto {
  enabled: boolean;
  aliases: string[];
  onDemandLoad: boolean;
  loadWaitS: number;
  firstByteTimeoutS: number | null;
  preferredNodes: string[];
  /** False while the model has no stored row (the defaults apply). */
  stored: boolean;
  updatedAt: string | null;
}

export type ModelLiveState = 'loaded' | 'loadable' | 'unloaded' | 'none';

export interface ModelNodeDto {
  nodeId: string;
  status: 'loaded' | 'loadable';
}

/**
 * One catalog model with its policy and its live state.
 *  - `state`: `loaded` (a healthy node has it loaded), `loadable` (none has, on-demand is on and a node could load it:
 *    the states of `/v1/models`), `unloaded` (a node has it but the policy does not offer it, or it is disabled), `none`
 *    (no routable node has it).
 *  - `listed`: whether `/v1/models` lists it for a key with no scope.
 */
export interface ModelRowDto {
  modelId: string;
  displayName: string;
  roles: string[];
  policy: ModelPolicyDto;
  state: ModelLiveState;
  listed: boolean;
  nodes: ModelNodeDto[];
}
