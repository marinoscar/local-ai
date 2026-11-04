// The wire shapes of `GET /api/localai/audit` and `GET /api/localai/audit/filters` (docs/specs/cloud-app.md section
// 3.9, "As built: audit"). Mirrors apps/api/src/localai/audit/audit.schemas.ts; the shared fixture
// apps/api/test/fixtures/localai/audit-page.json is proven against the API and rendered by the web tests.
export type AuditActorKind = 'user' | 'node' | 'system';

export interface AuditRow {
  id: string;
  /** ISO instant. */
  at: string;
  actor: { kind: AuditActorKind; label: string };
  action: string;
  targetType: string;
  targetId: string;
  summary: string;
  /** An in-app path, or null. */
  link: string | null;
}

export interface AuditPageData {
  items: AuditRow[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

export interface AuditFilterOptions {
  groups: Array<{ prefix: string; label: string }>;
  actions: Array<{ action: string; label: string }>;
  nodes: Array<{ nodeId: string; displayName: string; phase: string }>;
  pageSize: number;
  defaultWindowDays: number;
}

/** What the filter bar holds; every field is optional text, `from` and `to` are calendar days (`2026-11-02`). */
export interface AuditFilterState {
  nodeId: string;
  action: string;
  from: string;
  to: string;
  page: number;
}

export const EMPTY_FILTERS: AuditFilterState = { nodeId: '', action: '', from: '', to: '', page: 1 };
