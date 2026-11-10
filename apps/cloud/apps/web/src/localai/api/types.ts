// The wire shapes the node detail page reads (docs/specs/cloud-app.md sections 3.7, 3.9 and 3.11). `NodeCardDto` is the
// shared card (`@localai/ui`); the rest mirrors `apps/api/src/localai/nodes/dto/node-card.ts` and
// `commands/command-view.ts`. Plain data, nothing here fetches. The snapshot is typed loosely on purpose: it is the
// agent's schema (`contracts/state.schema.ts`), the page reads only the parts a tab shows and tolerates a missing one.
import type { NodeCardDto } from '@localai/ui';

export type ServePattern = 'A' | 'B' | 'none';

export interface NodeSettings {
  publicHost: string;
  servePattern: ServePattern;
  tailnetMachine: string | null;
  tailnetIpSet: boolean;
  gatewayEnabled: boolean;
  gatewayPriority: number;
  catalogPin: string | null;
  profile: string | null;
  connectivity: string | null;
}

export interface NodeKeys {
  /** `ab12…9z0f`: the first and last four of the node's llama key. Never more. */
  keyHint: string | null;
  keyReportedAt: string | null;
  credential: { prefix: string; lastUsedAt: string | null; createdAt: string } | null;
}

export interface SnapshotModel {
  id: string;
  present: boolean;
  verified: boolean;
  loadable: boolean;
  loadable_reason: string | null;
  status: 'loaded' | 'loading' | 'unloaded' | 'failed';
  default: boolean;
  roots?: string[];
}

export interface SnapshotService {
  name: 'llama' | 'kiwix' | 'whisper' | 'sync' | 'drill';
  active_state: 'active' | 'inactive' | 'failed' | 'activating' | 'deactivating' | 'not_installed';
  since: string | null;
}

export interface SnapshotArkRoot {
  id: string;
  present: boolean;
  tiers: { t1: string; t2: string; t3: string };
  items_missing: number;
  items_failed: number;
  items_skipped_budget: number;
  bytes_used: number;
  bytes_free: number;
  last_sync_at: string | null;
  last_verify_at: string | null;
}

/** The `ensure` block of a snapshot (contracts/state.schema.ts `EnsureBlockV1`): absent from an agent that predates it. */
export interface SnapshotEnsure {
  state: 'complete' | 'installing' | 'needs_you' | 'off';
  auto: 'on' | 'ask' | 'off';
  message: string;
  progress: { done: number; total: number };
  next_action: { kind: 'none' | 'wait' | 'press_button' | 'confirm' | 'sync' | 'fix'; step: string | null };
  steps: Array<{ id: string; status: 'ok' | 'pending' | 'waiting' | 'needs_confirm' | 'blocked' | 'running' | 'done' | 'failed' | 'cancelled' }>;
}

export interface NodeSnapshot {
  mode?: string;
  llama?: { build?: string; backend?: string; router?: string; models_max?: number };
  hardware?: { gpus?: Array<{ name: string; vram_total_mib: number; vram_used_mib: number }> };
  models?: SnapshotModel[];
  services?: SnapshotService[];
  ark?: { roots?: SnapshotArkRoot[]; drive_last_synced_at?: string | null };
  ensure?: SnapshotEnsure;
  sync?: { running?: boolean; progress_pct?: number | null; last_run?: { id: string; finished_at: string; result: string; skip_reason: string | null; error_code: string | null } | null };
}

/** `GET /api/localai/nodes/:nodeId`. */
export type NodeDetail = NodeCardDto & {
  lastState: NodeSnapshot | null;
  /** Present only after a successful `?reveal=tailnet` by a lab:admin. */
  tailnetIp?: string | null;
  settings: NodeSettings;
  keys: NodeKeys;
  /** The command types the node allows (no `localai.` prefix); null when the cloud cannot tell. */
  acceptedTypes: string[] | null;
  /** Model id to planned download bytes, from the approved catalog (best effort). */
  modelSizes: Record<string, number>;
  agentLatestVersion: string | null;
};

export type CommandStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'expired' | 'cancelled';
export const ACTIVE_STATUSES: readonly CommandStatus[] = ['queued', 'running'];

export interface CommandError {
  code?: string;
  message?: string;
}

/** `GET /api/localai/commands/:id` (the history leaves `result` out). */
export interface CommandView {
  id: string;
  nodeId: string;
  /** The full registry name, `localai.model.load`. */
  type: string;
  status: CommandStatus;
  params: Record<string, unknown>;
  source: string;
  expiresAt: string;
  createdAt: string;
  claimedAt: string | null;
  finishedAt: string | null;
  error: CommandError | null;
  result?: Record<string, unknown> | null;
}

export interface CommandPage {
  items: CommandView[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

/** `POST …/credential`: the token appears here once and nowhere else. */
export interface ReplacedCredential {
  nodeId: string;
  token: string;
  tokenPrefix: string;
  credentialId: string;
  createdAt: string;
  reenroll: string[];
}

export interface RetiredNode {
  nodeId: string;
  phase: 'retired';
  cancelledCommands: number;
}

export interface PatchNodeInput {
  displayName?: string;
  publicHost?: string;
  servePattern?: ServePattern;
  tailnetMachine?: string | null;
  tailnetIp?: string | null;
  gatewayEnabled?: boolean;
  gatewayPriority?: number;
  catalogPin?: string | null;
}
