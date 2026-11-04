// The Catalog page's transport: the routes of docs/specs/cloud-app.md 3.9 under `/api/localai/catalog/*`, typed as the
// API answers them (`apps/api/src/localai/catalog/catalog.service.ts`). The page and its hooks take a `CatalogApi`, so
// the tests (and the render checks) replace the network with plain functions.
import { ApiError } from '@marinoscar/platform-web/core';

import { api } from '../../api';

export type CatalogSource = 'github_release' | 'editor' | 'upload';
export type CatalogStatus = 'draft' | 'invalid' | 'valid' | 'approved' | 'superseded';

export interface LintProblem {
  code: string;
  file: string;
  path: string;
  message: string;
}

/** Why a lint run did not produce a verdict (`LAI-CLD-033`, the one code for every such reason). */
export interface LintFailure {
  code: string;
  reason: string;
  message: string;
}

export interface LintResult {
  valid?: boolean;
  problems?: LintProblem[];
  error?: LintFailure;
}

export interface CatalogFile {
  path: string;
  sha256: string;
  content: string;
}

export interface CatalogVersionSummary {
  id: string;
  version: string;
  source: CatalogSource;
  sourceRef: string | null;
  status: CatalogStatus;
  bundleSha256: string;
  lintedAt: string | null;
  /** The number of problems the last lint found. */
  problemCount: number;
  lintError: LintFailure | null;
  notes: string | null;
  createdById: string | null;
  createdAt: string;
  approvedById: string | null;
  /** The approver's display name; null when unknown. */
  approvedByName: string | null;
  approvedAt: string | null;
}

export interface FileDiff {
  path: string;
  change: 'added' | 'removed' | 'changed' | 'unchanged';
  baseSha256: string | null;
  sha256: string | null;
}

export interface CatalogVersionDetail extends CatalogVersionSummary {
  files: CatalogFile[];
  lintResult: LintResult | null;
  /** With `?diff=approved`: `against` is null when nothing is approved. */
  diff?: { against: string | null; files: FileDiff[] };
}

export interface CreateDraftBody {
  version: string;
  baseVersion?: string;
  files: Array<{ path: string; content: string }>;
  notes?: string;
  source?: 'editor' | 'upload';
}

export interface ApproveResponse {
  version: CatalogVersionSummary;
  supersedes: string | null;
  notified: { queued: string[]; alreadyQueued: string[]; skipped: number } | null;
}

export interface CatalogApi {
  list(): Promise<CatalogVersionSummary[]>;
  get(version: string, diff?: 'approved'): Promise<CatalogVersionDetail>;
  importRelease(tag: string): Promise<{ tag: string; version: string; jobId: string }>;
  createDraft(body: CreateDraftBody): Promise<CatalogVersionSummary>;
  lint(version: string): Promise<{ version: string; jobId: string }>;
  approve(version: string, notifyNodes: boolean): Promise<ApproveResponse>;
  /** Queues `catalog.refresh` on one node (the command plane, `POST /api/localai/nodes/:nodeId/commands`). */
  refreshNode(nodeId: string): Promise<void>;
}

const BASE = '/localai/catalog';

export const catalogApi: CatalogApi = {
  list: () => api.get<CatalogVersionSummary[]>(`${BASE}/versions`),
  get: (version, diff) => api.get<CatalogVersionDetail>(`${BASE}/versions/${encodeURIComponent(version)}${diff ? `?diff=${diff}` : ''}`),
  importRelease: (tag) => api.post(`${BASE}/import`, { tag }),
  createDraft: (body) => api.post(`${BASE}/versions`, body),
  lint: (version) => api.post(`${BASE}/versions/${encodeURIComponent(version)}/lint`),
  approve: (version, notifyNodes) => api.post(`${BASE}/versions/${encodeURIComponent(version)}/approve`, { notifyNodes }),
  refreshNode: async (nodeId) => {
    await api.post(`/localai/nodes/${encodeURIComponent(nodeId)}/commands`, { type: 'catalog.refresh', params: {} });
  },
};

/** The `details.reason` the catalog routes put on a refusal (`version_exists`, `version_not_greater`, ...), if any. */
export function errorReason(error: unknown): string | null {
  if (!(error instanceof ApiError)) return null;
  const details = error.details as { reason?: unknown } | undefined;
  return typeof details?.reason === 'string' ? details.reason : null;
}

export function errorStatus(error: unknown): number | null {
  return error instanceof ApiError ? error.status : null;
}

/** The sentence to show for a failed call: the server's own message when it sent one. */
export function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message && error.message !== 'Request failed' ? error.message : fallback;
}
