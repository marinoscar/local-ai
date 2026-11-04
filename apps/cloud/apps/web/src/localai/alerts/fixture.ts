// The alerts of the wireframe (docs/specs/cloud-app.md section 3.11), as the API returns them. Invented lab nodes and
// doctor codes from the registry; no real data.
import type { AlertDto, AlertsResponse } from './types';

export const ALERTS_NOW = new Date('2026-11-04T12:00:00.000Z');

const base = { clearedAt: null, acknowledgedAt: null };

export function alertsFixture(): AlertsResponse {
  const alerts: AlertDto[] = [
    {
      ...base,
      id: '00000000-0000-4000-8000-000000000001',
      nodeId: 'ai-n2',
      nodeName: 'Node Two',
      kind: 'ark.drive_stale',
      code: 'LAI-CLD-022',
      severity: 'critical',
      state: 'active',
      title: 'Ark drive not synced for 95 days',
      fix: 'Plug the AI-ARK drive into the node and press "Sync now" (or run `aictl ark sync`).',
      firstRaisedAt: '2026-11-04T00:00:00.000Z',
      lastSeenAt: '2026-11-04T11:55:00.000Z',
    },
    {
      ...base,
      id: '00000000-0000-4000-8000-000000000002',
      nodeId: 'ai-n2',
      nodeName: 'Node Two',
      kind: 'sync.failed',
      code: 'LAI-CLD-023',
      severity: 'warning',
      state: 'active',
      title: 'Last ark sync failed (LAI-SYN-031)',
      fix: "Open the node's Ark tab, read the sync error code and follow its fix, then press \"Sync now\".",
      firstRaisedAt: '2026-11-04T06:00:00.000Z',
      lastSeenAt: '2026-11-04T11:55:00.000Z',
      acknowledgedAt: '2026-11-04T09:00:00.000Z',
    },
    {
      ...base,
      id: '00000000-0000-4000-8000-000000000003',
      nodeId: 'ai-n3',
      nodeName: 'Node Three',
      kind: 'node.stale',
      code: 'LAI-CLD-020',
      severity: 'warning',
      state: 'active',
      title: 'Not seen for 3 h',
      fix: 'Check the computer is on and connected. If it was moved to Offline mode on purpose, ignore this.',
      firstRaisedAt: '2026-11-04T09:00:00.000Z',
      lastSeenAt: '2026-11-04T11:55:00.000Z',
    },
    {
      ...base,
      id: '00000000-0000-4000-8000-000000000004',
      nodeId: 'ai-n1',
      nodeName: 'Node One',
      kind: 'agent.outdated',
      code: 'LAI-CLD-029',
      severity: 'info',
      state: 'active',
      title: 'Agent 0.4.0 is behind 0.4.1',
      fix: 'Admin: Lab → node → Update agent.',
      firstRaisedAt: '2026-11-03T12:00:00.000Z',
      lastSeenAt: '2026-11-04T11:55:00.000Z',
    },
  ];
  return { alerts };
}

export function resolvedFixture(): AlertsResponse {
  return {
    alerts: [
      {
        ...base,
        id: '00000000-0000-4000-8000-000000000005',
        nodeId: 'ai-n1',
        nodeName: 'Node One',
        kind: 'drill.failed',
        code: 'LAI-CLD-024',
        severity: 'critical',
        state: 'cleared',
        title: 'Monthly drill failed: LAI-DRL-020',
        fix: "Open the node in the cloud or run `aictl drill` on it and follow the failed check's fix.",
        firstRaisedAt: '2026-11-01T10:00:00.000Z',
        lastSeenAt: '2026-11-03T10:00:00.000Z',
        clearedAt: '2026-11-03T10:30:00.000Z',
      },
    ],
  };
}
