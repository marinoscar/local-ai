// The audit rows behind `fixtures/localai/audit-page.json`: one `audit_events` row per kind of line the page shows
// (a user, a node and the system as actor; node, command, catalog, alert and gateway targets; an unknown action; meta
// that tries to carry a secret). `audit-fixture.spec.ts` runs the real service over them and proves the JSON; the web
// tests render that JSON, so what the page tests show is what the server computes. Secret-looking strings are built
// at run time, never written as literals.
export const AUDIT_FIXTURE_NOW = new Date('2026-11-04T12:00:00.000Z');
export const AUDIT_FIXTURE_TOTAL = 123;
export const ADMIN_ID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
export const OPERATOR_ID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2';
export const AUDIT_FIXTURE_USERS = [
  { id: ADMIN_ID, email: 'admin@example.test' },
  { id: OPERATOR_ID, email: 'operator@example.test' },
];

const CMD = '6b3c1c9e-7a52-4f4e-9d5e-0a1b2c3d4e5f';
const CMD_2 = '7c4d2dae-8b63-4a5f-8e6f-1b2c3d4e5f60';
const ALERT = '4a1e5c2d-9b7f-4d3a-8c6e-1f2a3b4c5d6e';
const at = (time: string) => new Date(`2026-11-04T${time}.000Z`);
const node = (action: string, targetId: string, meta: Record<string, unknown>, actorUserId: string | null = ADMIN_ID) => ({ action, targetType: 'localai_node', targetId, meta, actorUserId });

/** Newest first, the order the API answers in. */
export function auditFixtureEvents() {
  const token = `nod_${'a1B2'.repeat(8)}`;
  const rows = [
    { time: '11:58:12', ...{ action: 'localai.command.settled', targetType: 'localai_command', targetId: CMD, actorUserId: OPERATOR_ID, meta: { nodeId: 'ai-n2', type: 'localai.model.load', status: 'succeeded' } } },
    { time: '11:57:41', ...{ action: 'localai.command.requested', targetType: 'localai_command', targetId: CMD, actorUserId: OPERATOR_ID, meta: { nodeId: 'ai-n2', type: 'localai.model.load', params: JSON.stringify({ model_id: 'qwen3.6-35b-a3b-q4km', wait: true }) } } },
    { time: '11:40:03', ...node('localai.node.mode_changed', 'ai-n3', { from: 'online', to: 'offline' }) },
    { time: '11:12:50', ...{ action: 'localai.command.settled', targetType: 'localai_command', targetId: CMD_2, actorUserId: OPERATOR_ID, meta: { nodeId: 'ai-n1', type: 'localai.service.restart', status: 'failed', errorCode: 'LAI-LLM-011' } } },
    { time: '10:30:00', ...{ action: 'localai.catalog.approved', targetType: 'localai_catalog', targetId: '2026.11.04-1', actorUserId: ADMIN_ID, meta: { source: 'github_release', sourceRef: 'catalog-2026.11.04-1', nodesNotified: 2 } } },
    { time: '10:05:44', ...node('localai.node.enrolled', 'ai-n4', { workerNodeId: CMD, agentVersion: '0.4.1' }) },
    { time: '09:15:20', ...node('localai.node.binding_refused', 'ai-n2', { credentialPrefix: token.slice(0, 8), claimedNodeId: 'ai-n2' }) },
    { time: '09:02:11', ...{ action: 'localai.alert.acknowledged', targetType: 'localai_alert', targetId: ALERT, actorUserId: ADMIN_ID, meta: { kind: 'ark.drive_stale', nodeId: 'ai-n2' } } },
    { time: '08:30:00', ...{ action: 'localai.gateway.key_created', targetType: 'localai_gateway_key', targetId: 'gwk-1', actorUserId: ADMIN_ID, meta: { prefix: 'lai_ab12', name: 'Desk laptop' } } },
    // An action this build does not know, and a node target whose meta tries to carry a secret and a tailnet address.
    { time: '08:00:00', ...{ action: 'localai.future.thing', targetType: 'localai_future', targetId: 'f-1', actorUserId: null, meta: { authorization: `Bearer ${token}`, address: ['100', '88', '4', '9'].join('.') } } },
  ];
  return rows.map(({ time, ...row }, index) => ({ id: `cccccccc-cccc-4ccc-8ccc-${String(index + 1).padStart(12, '0')}`, orgId: null, createdAt: at(time), ...row }));
}
