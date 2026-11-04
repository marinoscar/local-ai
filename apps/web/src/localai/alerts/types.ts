// The wire shape of `GET /api/localai/alerts` and `POST /api/localai/alerts/:id/ack` (docs/specs/cloud-app.md section
// 3.9), as the API returns it (`apps/api/src/localai/alerts/alerts.schemas.ts`). The page renders what it is given; the
// fix text is the API's, from the doctor code registry.
export type AlertSeverity = 'info' | 'warning' | 'critical';
export type AlertState = 'active' | 'cleared';
export type AlertStateFilter = AlertState | 'all';

export interface AlertDto {
  id: string;
  /** The lab node id (`ai-n2`), or null for a lab-wide alert. */
  nodeId: string | null;
  nodeName: string | null;
  kind: string;
  code: string;
  severity: AlertSeverity;
  state: AlertState;
  title: string;
  /** The plain-language fix. */
  fix: string;
  firstRaisedAt: string;
  lastSeenAt: string;
  clearedAt: string | null;
  acknowledgedAt: string | null;
}

export interface AlertsResponse {
  alerts: AlertDto[];
}

export interface AlertFilters {
  state: AlertStateFilter;
  /** A lab node id, or '' for every node. */
  nodeId: string;
}
