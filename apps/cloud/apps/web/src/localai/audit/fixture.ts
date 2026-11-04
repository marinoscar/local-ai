// The audit page as the API answers it: the same file the API's fixture test proves against the real service
// (apps/api/test/fixtures/localai/audit-page.json), so what these tests render is what the server computes.
import type { AuditFilterOptions, AuditPageData } from './types';

import raw from '../../../../api/test/fixtures/localai/audit-page.json';

/** A fresh deep copy: a test may edit it. */
export const auditFixture = (): AuditPageData => structuredClone(raw) as AuditPageData;

/** What `GET /api/localai/audit/filters` answers for a three-node lab (ai-n4 retired). */
export const filterFixture = (): AuditFilterOptions => ({
  groups: [
    { prefix: 'localai.node.', label: 'Nodes' },
    { prefix: 'localai.command.', label: 'Commands' },
    { prefix: 'localai.catalog.', label: 'Catalog' },
    { prefix: 'localai.checklist.', label: 'Publish checklist' },
    { prefix: 'localai.alert.', label: 'Alerts' },
    { prefix: 'localai.gateway.', label: 'Gateway' },
  ],
  actions: [
    { action: 'localai.node.created', label: 'Node added' },
    { action: 'localai.node.enrolled', label: 'Node enrolled' },
    { action: 'localai.node.mode_changed', label: 'Mode changed' },
    { action: 'localai.command.requested', label: 'Command requested' },
    { action: 'localai.command.settled', label: 'Command finished' },
    { action: 'localai.catalog.approved', label: 'Catalog approved' },
    { action: 'localai.alert.acknowledged', label: 'Alert acknowledged' },
    { action: 'localai.gateway.key_created', label: 'Gateway key created' },
  ],
  nodes: [
    { nodeId: 'ai-n1', displayName: 'MS-S1 Max', phase: 'enrolled' },
    { nodeId: 'ai-n2', displayName: 'MSI Stealth', phase: 'enrolled' },
    { nodeId: 'ai-n3', displayName: 'HP ZBook', phase: 'enrolled' },
    { nodeId: 'ai-n4', displayName: 'Old box', phase: 'retired' },
  ],
  pageSize: 50,
  defaultWindowDays: 30,
});
