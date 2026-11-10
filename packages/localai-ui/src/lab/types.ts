// The plain data the Lab components take. `NodeCardDto` is the wire shape of `GET /api/localai/nodes`
// (docs/specs/cloud-app.md section 3.9), mirrored here so the cloud page and the desktop Lab view (DSK-11) share it; the
// cloud API's `apps/api/src/localai/nodes/dto/node-card.ts` holds the Zod schema and a contract test keeps the two
// equal. Everything derived (health, routable, ark colours, behind flags) is computed by the API: components render it,
// they never recompute it.

export type LabHealth = 'healthy' | 'degraded' | 'down' | 'quiet';
export type LabMode = 'online' | 'offline' | 'survival';
export type LabPhase = 'pending' | 'enrolled' | 'retired';
export type ArkColour = 'green' | 'amber' | 'red' | 'grey';
export type SyncResult = 'ok' | 'partial' | 'failed' | 'skipped';
export type DrillResult = 'pass' | 'warn' | 'fail';

export interface LoadedModel {
  id: string;
  isDefault: boolean;
}

export interface ArkRoot {
  rootId: string;
  kind: 'internal' | 'ark_drive';
  present: boolean;
  colour: ArkColour;
  label: string;
  driveAgeDays: number | null;
}

/**
 * "Install what's missing" at a glance (ADR-0010, issue #480): the state of the node's own kit installs, in codes. The
 * words are written here in the viewer's language (`EnsureLine`), never taken from the node. `nextStep` is a step id such
 * as `tool.piper`.
 */
export interface EnsureCard {
  state: 'complete' | 'installing' | 'needs_you' | 'off';
  auto: 'on' | 'ask' | 'off';
  done: number;
  total: number;
  nextKind: 'none' | 'wait' | 'press_button' | 'confirm' | 'sync' | 'fix';
  nextStep: string | null;
}

export interface NodeCardDto {
  nodeId: string;
  displayName: string;
  hardware: string | null;
  backend: 'cuda' | 'vulkan' | 'cpu' | null;
  phase: LabPhase;
  mode: LabMode | null;
  health: LabHealth;
  routable: boolean;
  lastStateAt: string | null;
  loadedModels: LoadedModel[];
  lastDefault: string | null;
  vram: { usedMib: number; totalMib: number } | null;
  ark: ArkRoot[];
  sync: { result: SyncResult | null; finishedAt: string | null; skipReason: string | null };
  drill: { result: DrillResult | null };
  agentVersion: string | null;
  agentBehind: boolean;
  catalogVersion: string | null;
  catalogBehind: boolean;
  alerts: { active: number; critical: number };
  /** Absent from an older cloud, `null` for a node that has not reported one (an agent that predates it). */
  ensure?: EnsureCard | null;
}

/** The header counts of the Lab page. */
export interface LabSummaryDto {
  nodes: number;
  online: number;
  offlineMode: number;
  activeAlerts: number;
  /** The "Gateway: N nodes" figure. */
  routable: number;
}

export interface LabNodesResponse {
  summary: LabSummaryDto;
  nodes: NodeCardDto[];
}
