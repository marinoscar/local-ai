// The alert vocabulary of the Local AI cloud (docs/specs/cloud-app.md section 4.9): the kinds, their doctor codes and the
// plain-language fix that goes with each. PURE data. A kind string and an event key are PERSISTED (alert rows, audit
// events, notification preferences): add a new one, never rename one.
//
// The fix text mirrors the "Plain-language fix" column of the `LAI-CLD` table in docs/specs/doctor-codes.md (section 2)
// word for word; `alert-kinds.spec.ts` keeps the two in step. The Doctor's shared map (`../doctor/codes.ts`, CLOUD-17)
// holds only the codes the Doctor checks raise, so the other fixes are kept here and LAI-CLD-026 is taken from it.

import { CLOUD_CODES } from '../doctor/codes';

export type AlertSeverity = 'info' | 'warning' | 'critical';
export type AlertState = 'active' | 'cleared';

/** The kinds the evaluator owns: it raises them and it clears them. */
export const EVALUATOR_KINDS = [
  'node.stale',
  'node.offline',
  'ark.drive_stale',
  'sync.failed',
  'drill.failed',
  'ark.tier1_missing',
  'key.missing',
  'catalog.behind',
  'agent.outdated',
] as const;
export type EvaluatorKind = (typeof EVALUATOR_KINDS)[number];

/**
 * Kinds whose truth is owned elsewhere: raised and cleared through `AlertsService.raiseExternal` / `clearExternal`
 * (`endpoint.down` by the gateway probe, GW-04). The evaluator never clears one by itself.
 */
export const EXTERNAL_KINDS = ['endpoint.down'] as const;
export type ExternalKind = (typeof EXTERNAL_KINDS)[number];

/** `node.alerts.<code>`: a critical alert the node reported in its own snapshot, one row per code. */
export const NODE_REPORTED_PREFIX = 'node.alerts.';

export function isNodeReportedKind(kind: string): boolean {
  return kind.startsWith(NODE_REPORTED_PREFIX);
}

export function isExternalKind(kind: string): kind is ExternalKind {
  return (EXTERNAL_KINDS as readonly string[]).includes(kind);
}

/** A kind the evaluator may clear: its own kinds and every node-reported one, never an external kind. */
export function isEvaluatorOwnedKind(kind: string): boolean {
  return (EVALUATOR_KINDS as readonly string[]).includes(kind) || isNodeReportedKind(kind);
}

/** The doctor code of each cloud-side kind (docs/specs/doctor-codes.md section 2, `LAI-CLD`). */
export const KIND_CODES: Readonly<Record<EvaluatorKind | ExternalKind, string>> = {
  'node.stale': 'LAI-CLD-020',
  'node.offline': 'LAI-CLD-021',
  'ark.drive_stale': 'LAI-CLD-022',
  'sync.failed': 'LAI-CLD-023',
  'drill.failed': 'LAI-CLD-024',
  'ark.tier1_missing': 'LAI-CLD-025',
  'key.missing': 'LAI-CLD-026',
  'endpoint.down': 'LAI-CLD-027',
  'catalog.behind': 'LAI-CLD-028',
  'agent.outdated': 'LAI-CLD-029',
};

/** The plain fix for each cloud-side code, as written in the doctor code registry. */
export const CODE_FIXES: Readonly<Record<string, string>> = {
  'LAI-CLD-020': 'Check the computer is on and connected. If it was moved to Offline mode on purpose, ignore this',
  'LAI-CLD-021': 'Turn the computer on or check its network. If it is retired, retire it in the cloud',
  'LAI-CLD-022': 'Plug the AI-ARK drive into the node and press "Sync now" (or run `aictl ark sync`)',
  'LAI-CLD-023': "Open the node's Ark tab, read the sync error code and follow its fix, then press \"Sync now\"",
  'LAI-CLD-024': "Open the node in the cloud or run `aictl drill` on it and follow the failed check's fix",
  'LAI-CLD-025': 'Press "Sync now" while online, or import from the AI-ARK drive (`aictl ark import`)',
  // The Doctor checks (CLOUD-17) keep one map of the codes they use; the alert answers with the same words.
  [CLOUD_CODES.nodeKeyMissing.code]: CLOUD_CODES.nodeKeyMissing.fix,
  'LAI-CLD-027': 'Check `tailscale serve status` on the node and the vhost on the VPS (runbook 05)',
  'LAI-CLD-028': 'Press "Refresh catalog" on the node',
  'LAI-CLD-029': 'Admin: Lab → node → Update agent',
};

/** Shown for a code the cloud does not know the fix of (a node-reported code): the node's own title says what is wrong. */
export const GENERIC_FIX = "Open the node's page in the cloud and follow the fix for this code in the doctor codes list.";

/** The plain-language fix of a doctor code. Never empty. */
export function plainFix(code: string): string {
  return CODE_FIXES[code] ?? GENERIC_FIX;
}

/** The severity order, most urgent first, for sorting and for "did it escalate". */
export const SEVERITY_RANK: Readonly<Record<AlertSeverity, number>> = { critical: 3, warning: 2, info: 1 };

export function isAlertSeverity(value: unknown): value is AlertSeverity {
  return value === 'info' || value === 'warning' || value === 'critical';
}

/** A doctor code as the snapshot schema accepts it (`LAI-<AREA>-<NNN>`). */
export const DOCTOR_CODE_PATTERN = /^LAI-[A-Z]{2,5}-\d{3}$/;
