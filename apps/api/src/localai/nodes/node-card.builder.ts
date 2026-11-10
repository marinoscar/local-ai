// Builds a `NodeCardDto` from the stored rows (docs/specs/cloud-app.md section 3.9). PURE: no database, no clock, no
// settings reads; `lab-nodes.service.ts` gathers the rows and `now`, this decides. Everything derived (health, routable,
// ark colours, behind flags) is computed here and ONLY here: the web app renders what it is given.
//
// `lastState` is the verbatim JSON of the last accepted snapshot, validated at ingestion. It is read defensively here
// (type guards, never a throw): a card for a node must still render if an older snapshot lacks a field.
import type { NodeHealth } from '@marinoscar/platform-api/nodes';

import { arkColour, arkLabel, wholeDaysSince, type ArkRootInput, type ArkThresholds, type ArkTier, type SyncResult } from '../health/ark-colour';
import { deriveHealth, isRoutable, type LabMode, type LabPhase, type LabServePattern } from '../health/derive-health';
import { isAgentBehind, isCatalogBehind } from '../health/versions';
import type { EnsureCardDto, NodeCardDto } from './dto/node-card';

/** The `localai_nodes` columns a card reads (a structural subset of the Prisma row). */
export interface CardNodeRow {
  nodeId: string;
  displayName: string;
  hardware: string | null;
  backend: string | null;
  phase: LabPhase;
  mode: string | null;
  servePattern: LabServePattern;
  gatewayEnabled: boolean;
  agentVersion: string | null;
  catalogVersion: string | null;
  keyReportedAt: Date | null;
  lastState: unknown;
  lastStateAt: Date | null;
}

export interface CardModelRow {
  modelId: string;
  status: string;
  isDefault: boolean;
}

export interface CardContext {
  now: Date;
  thresholds: ArkThresholds;
  /** `settings.agent.latestVersion`, or null. */
  agentLatestVersion: string | null;
  /** The approved catalog version, or null when none is approved. */
  approvedCatalogVersion: string | null;
}

export interface CardSource {
  node: CardNodeRow;
  models: readonly CardModelRow[];
  /** EAB's `deriveNodeHealth` of the linked worker row; `offline` when the node has no linked worker row. */
  eab: NodeHealth;
  alerts: { active: number; critical: number };
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const asString = (value: unknown): string | null => (typeof value === 'string' ? value : null);
const asCount = (value: unknown): number => (typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0);
const asTier = (value: unknown): ArkTier => (value === 'ok' || value === 'partial' || value === 'missing' ? value : 'missing');
const asDate = (value: unknown): Date | null => {
  if (typeof value !== 'string') return null;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
};

function asMode(value: string | null): LabMode | null {
  return value === 'online' || value === 'offline' || value === 'survival' ? value : null;
}

function asBackend(value: string | null): NodeCardDto['backend'] {
  return value === 'cuda' || value === 'vulkan' || value === 'cpu' ? value : null;
}

/** `ark-<8 hex>` is a drive, `internal` is the node's own disk (ark-sync.md section 2). */
function rootKind(rootId: string): 'internal' | 'ark_drive' {
  return rootId === 'internal' ? 'internal' : 'ark_drive';
}

/** "RTX 4080 12 GB": the first GPU's name without the vendor prefix, and the summed VRAM in whole GiB. */
function hardwareSummary(state: Record<string, unknown> | null): string | null {
  const hardware = state && isRecord(state.hardware) ? state.hardware : null;
  const gpus = hardware && Array.isArray(hardware.gpus) ? hardware.gpus.filter(isRecord) : [];
  if (gpus.length === 0) return null;
  const name = (asString(gpus[0]!.name) ?? '').replace(/^(NVIDIA|AMD|Intel)\s+(GeForce\s+|Radeon\s+)?/i, '').trim();
  const totalMib = gpus.reduce((sum, gpu) => sum + asCount(gpu.vram_total_mib), 0);
  const gib = Math.round(totalMib / 1024);
  return [name, gib > 0 ? `${gib} GB` : ''].filter((part) => part !== '').join(' ') || null;
}

function vramOf(state: Record<string, unknown> | null): NodeCardDto['vram'] {
  const hardware = state && isRecord(state.hardware) ? state.hardware : null;
  const gpus = hardware && Array.isArray(hardware.gpus) ? hardware.gpus.filter(isRecord) : [];
  const totalMib = gpus.reduce((sum, gpu) => sum + asCount(gpu.vram_total_mib), 0);
  if (totalMib === 0) return null;
  return { usedMib: gpus.reduce((sum, gpu) => sum + asCount(gpu.vram_used_mib), 0), totalMib };
}

const ENSURE_STATES: readonly string[] = ['complete', 'installing', 'needs_you', 'off'];
const ENSURE_AUTOS: readonly string[] = ['on', 'ask', 'off'];
const ENSURE_KINDS: readonly string[] = ['none', 'wait', 'press_button', 'confirm', 'sync', 'fix'];

/** The ensure block of the last snapshot as a card summary; null for a snapshot without one (an older agent) or a damaged one. */
function ensureOf(state: Record<string, unknown> | null): EnsureCardDto | null {
  const block = state && isRecord(state.ensure) ? state.ensure : null;
  if (block === null) return null;
  const stateName = asString(block.state);
  if (stateName === null || !ENSURE_STATES.includes(stateName)) return null;
  const auto = asString(block.auto);
  const progress = isRecord(block.progress) ? block.progress : {};
  const next = isRecord(block.next_action) ? block.next_action : {};
  const kind = asString(next.kind);
  return {
    state: stateName as EnsureCardDto['state'],
    auto: auto !== null && ENSURE_AUTOS.includes(auto) ? (auto as EnsureCardDto['auto']) : 'on',
    done: Math.floor(asCount(progress.done)),
    total: Math.floor(asCount(progress.total)),
    nextKind: kind !== null && ENSURE_KINDS.includes(kind) ? (kind as EnsureCardDto['nextKind']) : 'none',
    nextStep: asString(next.step),
  };
}

function arkOf(state: Record<string, unknown> | null, lastSyncResult: SyncResult | null, context: CardContext): NodeCardDto['ark'] {
  const { now, thresholds } = context;
  const ark = state && isRecord(state.ark) ? state.ark : null;
  if (ark === null) {
    // No snapshot yet: one grey row, so the card still has an Ark line.
    return [{ rootId: 'internal', kind: 'internal', present: false, colour: arkColour(null, thresholds), label: arkLabel(null, false, 'grey'), driveAgeDays: null }];
  }
  const driveLastSyncedAt = asDate(ark.drive_last_synced_at);
  const roots = (Array.isArray(ark.roots) ? ark.roots : []).filter(isRecord);
  const rows: NodeCardDto['ark'] = [];
  for (const root of roots) {
    const rootId = asString(root.id);
    if (rootId === null) continue;
    const kind = rootKind(rootId);
    const tiers = isRecord(root.tiers) ? root.tiers : {};
    const input: ArkRootInput = {
      kind,
      tiers: { t1: asTier(tiers.t1), t2: asTier(tiers.t2) },
      itemsMissing: asCount(root.items_missing),
      itemsFailed: asCount(root.items_failed),
      driveLastSyncedAt,
      lastSyncResult,
      now,
    };
    const present = root.present === true;
    const colour = arkColour(input, thresholds);
    rows.push({ rootId, kind, present, colour, label: arkLabel(input, present, colour), driveAgeDays: kind === 'ark_drive' ? wholeDaysSince(driveLastSyncedAt, now) : null });
  }
  // The last-seen drive stays on the card when it is unplugged and the agent no longer reports its root.
  if (driveLastSyncedAt !== null && !rows.some((row) => row.kind === 'ark_drive')) {
    const input: ArkRootInput = { kind: 'ark_drive', tiers: { t1: 'ok', t2: 'ok' }, itemsMissing: 0, itemsFailed: 0, driveLastSyncedAt, lastSyncResult, now };
    const colour = arkColour(input, thresholds);
    rows.push({ rootId: 'ark-drive', kind: 'ark_drive', present: false, colour, label: arkLabel(input, false, colour), driveAgeDays: wholeDaysSince(driveLastSyncedAt, now) });
  }
  // Internal first, then drives by id: a stable card regardless of the agent's order.
  return rows.sort((a, b) => (a.kind === b.kind ? a.rootId.localeCompare(b.rootId) : a.kind === 'internal' ? -1 : 1));
}

export function buildNodeCard(source: CardSource, context: CardContext): NodeCardDto {
  const { node, models, eab, alerts } = source;
  const state = isRecord(node.lastState) ? node.lastState : null;
  const mode = asMode(node.mode);

  const health = deriveHealth({ eab, phase: node.phase, mode, lastStateAt: node.lastStateAt, now: context.now });
  const routable = isRoutable({ health, mode, phase: node.phase, gatewayEnabled: node.gatewayEnabled, servePattern: node.servePattern, keyReportedAt: node.keyReportedAt });

  const sync = state && isRecord(state.sync) && isRecord(state.sync.last_run) ? state.sync.last_run : null;
  const syncResult = ((): SyncResult | null => {
    const value = sync ? asString(sync.result) : null;
    return value === 'ok' || value === 'partial' || value === 'failed' || value === 'skipped' ? value : null;
  })();
  const drillResult = ((): 'pass' | 'warn' | 'fail' | null => {
    const value = state && isRecord(state.drill) ? asString(state.drill.result) : null;
    return value === 'pass' || value === 'warn' || value === 'fail' ? value : null;
  })();

  const byName = (a: CardModelRow, b: CardModelRow) => Number(b.isDefault) - Number(a.isDefault) || a.modelId.localeCompare(b.modelId);
  const loaded = models.filter((model) => model.status === 'loaded').sort(byName);
  const defaultModel = models.find((model) => model.isDefault);

  return {
    nodeId: node.nodeId,
    displayName: node.displayName,
    hardware: node.hardware ?? hardwareSummary(state),
    backend: asBackend(node.backend),
    phase: node.phase,
    mode,
    health,
    routable,
    lastStateAt: node.lastStateAt ? node.lastStateAt.toISOString() : null,
    loadedModels: loaded.map((model) => ({ id: model.modelId, isDefault: model.isDefault })),
    lastDefault: defaultModel ? defaultModel.modelId : null,
    vram: vramOf(state),
    ark: arkOf(state, syncResult, context),
    sync: {
      result: syncResult,
      finishedAt: sync ? (asDate(sync.finished_at)?.toISOString() ?? null) : null,
      skipReason: sync ? asString(sync.skip_reason) : null,
    },
    drill: { result: drillResult },
    agentVersion: node.agentVersion,
    agentBehind: isAgentBehind(node.agentVersion, context.agentLatestVersion),
    catalogVersion: node.catalogVersion,
    catalogBehind: isCatalogBehind(node.catalogVersion, context.approvedCatalogVersion),
    alerts,
    ensure: ensureOf(state),
  };
}

/** Header counts. `labWideAlerts` are active alerts that belong to no node (they count in the header, on no card). */
export function summarize(cards: readonly NodeCardDto[], labWideAlerts: number) {
  return {
    nodes: cards.length,
    online: cards.filter((card) => card.mode === 'online' && (card.health === 'healthy' || card.health === 'degraded')).length,
    offlineMode: cards.filter((card) => card.mode === 'offline' || card.mode === 'survival').length,
    activeAlerts: cards.reduce((sum, card) => sum + card.alerts.active, 0) + labWideAlerts,
    routable: cards.filter((card) => card.routable).length,
  };
}

/** Node ids sort numerically (`ai-n2` before `ai-n10`); anything unparsable goes last, by text. */
export function compareNodeIds(a: string, b: string): number {
  const number = (id: string) => Number(/^ai-n(\d+)$/.exec(id)?.[1] ?? Number.POSITIVE_INFINITY);
  return number(a) - number(b) || a.localeCompare(b);
}
