import { AUDIT_NODE_CREDENTIAL_REVOKED, AUDIT_NODE_RETIRED, AUDIT_NODE_UPDATED } from '../nodes/nodes-admin.service';
import { AUDIT_NODE_TAILNET_REVEALED } from '../nodes/lab-nodes.service';
import { AUDIT_CHECKLIST_UPDATED } from '../wizard/checklist.service';
import { AUDIT_NODE_CREATED, AUDIT_NODE_CREDENTIAL_MINTED } from '../wizard/wizard.service';
import { AUDIT_NODE_BINDING_REFUSED, AUDIT_NODE_ENROLLED } from '../node-link/binding.guard';
import { AUDIT_NODE_MODE_CHANGED } from '../node-link/state.service';
import { AUDIT_NODE_KEY_REPORTED } from '../keys/node-key.service';
import { AUDIT_CATALOG_APPROVED, AUDIT_CATALOG_IMPORTED, AUDIT_CATALOG_LINTED } from '../catalog/catalog.constants';
import { AUDIT_COMMAND_CANCELLED, AUDIT_COMMAND_REQUESTED, AUDIT_COMMAND_SETTLED } from '../commands/command-audit';
import { AUDIT_GROUPS, KNOWN_AUDIT_ACTIONS, NODE_ACTOR_ACTIONS } from './audit-actions';
import { auditActor, describeAudit } from './audit-summary';

const CMD = '6b3c1c9e-7a52-4f4e-9d5e-0a1b2c3d4e5f';
const row = (action: string, targetType: string, targetId: string, meta: unknown) => ({ action, targetType, targetId, meta });

describe('the known audit actions', () => {
  it('every action sits under one of the groups, once', () => {
    const names = KNOWN_AUDIT_ACTIONS.map((def) => def.action);
    expect(new Set(names).size).toBe(names.length);
    for (const name of names) expect(AUDIT_GROUPS.some((group) => name.startsWith(group.prefix))).toBe(true);
  });

  it('lists every action the code writes today (their exported constants)', () => {
    const written = [
      AUDIT_NODE_UPDATED,
      AUDIT_NODE_RETIRED,
      AUDIT_NODE_CREDENTIAL_REVOKED,
      AUDIT_NODE_TAILNET_REVEALED,
      AUDIT_NODE_CREATED,
      AUDIT_NODE_CREDENTIAL_MINTED,
      AUDIT_CHECKLIST_UPDATED,
      AUDIT_NODE_ENROLLED,
      AUDIT_NODE_BINDING_REFUSED,
      AUDIT_NODE_MODE_CHANGED,
      AUDIT_NODE_KEY_REPORTED,
      AUDIT_CATALOG_IMPORTED,
      AUDIT_CATALOG_LINTED,
      AUDIT_CATALOG_APPROVED,
      AUDIT_COMMAND_REQUESTED,
      AUDIT_COMMAND_CANCELLED,
      AUDIT_COMMAND_SETTLED,
    ];
    const known = new Set(KNOWN_AUDIT_ACTIONS.map((def) => def.action));
    for (const action of written) expect(known.has(action)).toBe(true);
  });

  it('lists every action of the spec section 3.5 table', () => {
    const spec = [
      'created', 'updated', 'retired', 'credential_minted', 'credential_revoked', 'enrolled', 'binding_refused', 'mode_changed', 'tailnet_revealed', 'key_reported',
    ].map((name) => `localai.node.${name}`);
    spec.push('localai.command.requested', 'localai.command.cancelled', 'localai.command.settled');
    spec.push('localai.catalog.imported', 'localai.catalog.linted', 'localai.catalog.approved', 'localai.checklist.updated', 'localai.alert.acknowledged');
    spec.push('localai.gateway.key_created', 'localai.gateway.key_revoked', 'localai.gateway.key_updated', 'localai.gateway.policy_updated');
    const known = new Set(KNOWN_AUDIT_ACTIONS.map((def) => def.action));
    for (const action of spec) expect(known.has(action)).toBe(true);
  });

  it('the node-actor actions are known actions', () => {
    const known = new Set(KNOWN_AUDIT_ACTIONS.map((def) => def.action));
    for (const action of NODE_ACTOR_ACTIONS) expect(known.has(action)).toBe(true);
  });
});

describe('describeAudit: summary and link per action', () => {
  const ids = new Map([['eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee5', 'ai-n5']]);
  const cases: Array<[string, ReturnType<typeof row>, string, string | null]> = [
    ['node created (the wizard shape: serve pattern, public host, tailnet as <set>)', row('localai.node.created', 'localai_node', 'ai-n3', { servePattern: 'B', publicHost: 'ai-n3.example.test', tailnetIp: '<set>' }), 'Added node ai-n3 (ai-n3.example.test)', '/lab/nodes/ai-n3'],
    ['node created without a host', row('localai.node.created', 'localai_node', 'ai-n3', { servePattern: 'B' }), 'Added node ai-n3', '/lab/nodes/ai-n3'],
    ['node updated lists field names, never values', row('localai.node.updated', 'localai_node', 'ai-n3', { tailnetIp: '<set>', publicHost: 'ai-n3.example.test' }), 'Changed tailnetIp, publicHost on ai-n3', '/lab/nodes/ai-n3'],
    ['node updated without field names', row('localai.node.updated', 'localai_node', 'ai-n3', {}), 'Changed settings of ai-n3', '/lab/nodes/ai-n3'],
    ['node retired', row('localai.node.retired', 'localai_node', 'ai-n3', {}), 'Retired node ai-n3', '/lab/nodes/ai-n3'],
    ['node retired, commands cancelled', row('localai.node.retired', 'localai_node', 'ai-n3', { cancelledCommands: 2 }), 'Retired node ai-n3, 2 queued commands cancelled', '/lab/nodes/ai-n3'],
    ['node retired, one command cancelled', row('localai.node.retired', 'localai_node', 'ai-n3', { cancelledCommands: 1 }), 'Retired node ai-n3, 1 queued command cancelled', '/lab/nodes/ai-n3'],
    ['node updated (the route shape: values, tailnet as a word)', row('localai.node.updated', 'localai_node', 'ai-n3', { displayName: 'Node Three', gatewayPriority: 50, tailnetIp: '<cleared>' }), 'Changed displayName, tailnetIp, gatewayPriority on ai-n3', '/lab/nodes/ai-n3'],
    ['credential minted (the spec name, tokenPrefix)', row('localai.node.credential_minted', 'localai_node', 'ai-n3', { credentialId: CMD, tokenPrefix: 'nod_ab12cd34' }), 'Created credential nod_ab12cd34 for ai-n3', '/lab/nodes/ai-n3'],
    ['credential minted (the wizard name, credentialPrefix)', row('localai.node.credential_minted', 'localai_node', 'ai-n3', { credentialId: CMD, credentialPrefix: 'nod_ab12cd34' }), 'Created credential nod_ab12cd34 for ai-n3', '/lab/nodes/ai-n3'],
    ['credential revoked', row('localai.node.credential_revoked', 'localai_node', 'ai-n3', { tokenPrefix: 'nod_ab12cd34' }), 'Revoked credential nod_ab12cd34 of ai-n3', '/lab/nodes/ai-n3'],
    ['enrolled', row('localai.node.enrolled', 'localai_node', 'ai-n3', { workerNodeId: CMD, agentVersion: '0.4.1' }), 'ai-n3 enrolled with agent 0.4.1', '/lab/nodes/ai-n3'],
    ['binding refused names the claimed node', row('localai.node.binding_refused', 'localai_node', 'ai-n9', { credentialPrefix: 'nod_zz99yy88', claimedNodeId: 'ai-n9' }), 'Refused credential nod_zz99yy88 claiming ai-n9', '/lab/nodes/ai-n9'],
    ['mode changed', row('localai.node.mode_changed', 'localai_node', 'ai-n3', { from: 'online', to: 'offline' }), 'ai-n3 mode changed from online to offline', '/lab/nodes/ai-n3'],
    ['tailnet revealed', row('localai.node.tailnet_revealed', 'localai_node', 'ai-n3', {}), 'Tailnet address of ai-n3 revealed', '/lab/nodes/ai-n3'],
    ['key reported', row('localai.node.key_reported', 'localai_node', 'ai-n3', { keyHint: 'ab12…cd34', reason: 'rotate' }), 'ai-n3 reported its key (rotate) hint ab12…cd34', '/lab/nodes/ai-n3'],
    ['command requested (the issue example)', row('localai.command.requested', 'localai_command', CMD, { nodeId: 'ai-n2', type: 'localai.model.load', params: JSON.stringify({ model_id: 'qwen3.6-35b-a3b-q4km', wait: true }) }), 'model.load qwen3.6-35b-a3b-q4km on ai-n2', `/lab/nodes/ai-n2?command=${CMD}`],
    ['command cancelled', row('localai.command.cancelled', 'localai_command', CMD, { nodeId: 'ai-n2', type: 'localai.service.restart', params: '{"service":"localai-llama"}' }), 'Cancelled service.restart localai-llama on ai-n2', `/lab/nodes/ai-n2?command=${CMD}`],
    ['command settled ok', row('localai.command.settled', 'localai_command', CMD, { nodeId: 'ai-n2', type: 'localai.model.load', status: 'succeeded' }), 'model.load on ai-n2 succeeded', `/lab/nodes/ai-n2?command=${CMD}`],
    ['command settled failed with a code', row('localai.command.settled', 'localai_command', CMD, { nodeId: 'ai-n2', type: 'localai.model.load', status: 'failed', errorCode: 'LAI-LLM-011' }), 'model.load on ai-n2 failed (LAI-LLM-011)', `/lab/nodes/ai-n2?command=${CMD}`],
    ['catalog imported', row('localai.catalog.imported', 'localai_catalog', '2026.11.02-1', { source: 'github_release', sourceRef: 'catalog-2026.11.02-1' }), 'Imported catalog 2026.11.02-1 from github_release (catalog-2026.11.02-1)', '/lab/catalog?version=2026.11.02-1'],
    ['catalog linted ok', row('localai.catalog.linted', 'localai_catalog', '2026.11.02-1', { valid: true }), 'Catalog 2026.11.02-1 passed the check', '/lab/catalog?version=2026.11.02-1'],
    ['catalog linted bad', row('localai.catalog.linted', 'localai_catalog', '2026.11.02-1', { valid: false }), 'Catalog 2026.11.02-1 failed the check', '/lab/catalog?version=2026.11.02-1'],
    ['catalog approved', row('localai.catalog.approved', 'localai_catalog', '2026.11.02-1', { nodesNotified: 2 }), 'Approved catalog 2026.11.02-1, 2 nodes told to refresh', '/lab/catalog?version=2026.11.02-1'],
    ['checklist updated', row('localai.checklist.updated', 'localai_node', 'ai-n4', { step: 'dns', state: 'done' }), 'Checklist step dns of ai-n4 is now done', '/lab/nodes/ai-n4'],
    ['checklist updated (the wizard shape, with the action)', row('localai.checklist.updated', 'localai_node', 'ai-n4', { step: 'dns', action: 'check', state: 'done' }), 'Checklist step dns of ai-n4 is now done (check)', '/lab/nodes/ai-n4'],
    ['alert acknowledged', row('localai.alert.acknowledged', 'localai_alert', CMD, { kind: 'ark.drive_stale', nodeId: 'ai-n2' }), 'Acknowledged ark.drive_stale alert on ai-n2', '/lab/alerts'],
    ['gateway key created', row('localai.gateway.key_created', 'localai_gateway_key', CMD, { prefix: 'lai_ab12', name: 'Desk laptop' }), 'Gateway key lai_ab12 "Desk laptop" created', '/gateway/keys'],
    ['gateway key revoked', row('localai.gateway.key_revoked', 'localai_gateway_key', CMD, { prefix: 'lai_ab12' }), 'Gateway key lai_ab12 revoked', '/gateway/keys'],
    ['gateway policy', row('localai.gateway.policy_updated', 'localai_model_policy', 'qwen3.6-35b-a3b-q4km', { allowedModels: 'x' }), 'Gateway model policy changed', '/gateway/keys'],
    ['a node uuid target resolves to its lab id', row('localai.node.retired', 'localai_node', 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee5', {}), 'Retired node ai-n5', '/lab/nodes/ai-n5'],
    ['a node uuid with no match has no link', row('localai.node.retired', 'localai_node', 'ffffffff-ffff-4fff-8fff-fffffffffff6', {}), 'Retired node a node', null],
    ['an unknown action renders as its name', row('localai.widget.frobbed', 'localai_widget', 'w1', { secret: 'x' }), 'localai.widget.frobbed', null],
    ['an unknown action on a node still links', row('localai.node.future_thing', 'localai_node', 'ai-n3', {}), 'localai.node.future_thing', '/lab/nodes/ai-n3'],
    ['missing meta never throws', row('localai.command.requested', 'localai_command', CMD, null), 'command', null],
  ];

  it.each(cases)('%s', (_name, input, summary, link) => {
    const described = describeAudit(input, ids);
    expect(described.summary).toBe(summary);
    expect(described.link).toBe(link);
  });

  it('reports the node a row concerns', () => {
    expect(describeAudit(row('localai.command.requested', 'localai_command', CMD, { nodeId: 'ai-n2', type: 'localai.model.load' })).nodeId).toBe('ai-n2');
    expect(describeAudit(row('localai.catalog.approved', 'localai_catalog', '1', {})).nodeId).toBeNull();
  });
});

describe('describeAudit never prints a secret', () => {
  // Built at run time so no literal secret is committed.
  const token = `nod_${'a1B2'.repeat(8)}`;
  const hex = 'ab'.repeat(32);
  const tailnet = ['100', '88', '4', '9'].join('.');

  it('drops a token-shaped prefix, key, version or model id', () => {
    const rows = [
      row('localai.node.credential_minted', 'localai_node', 'ai-n3', { tokenPrefix: token }),
      row('localai.node.enrolled', 'localai_node', 'ai-n3', { agentVersion: hex }),
      row('localai.command.requested', 'localai_command', CMD, { nodeId: 'ai-n2', type: 'localai.model.load', params: JSON.stringify({ model_id: hex }) }),
      row('localai.node.key_reported', 'localai_node', 'ai-n3', { keyHint: hex, reason: 'enroll' }),
      row('localai.catalog.imported', 'localai_catalog', '2026.11.02-1', { source: 'upload', sourceRef: token }),
    ];
    for (const input of rows) {
      const { summary } = describeAudit(input);
      expect(summary).not.toContain(token);
      expect(summary).not.toContain(hex);
    }
  });

  it('prints only whitelisted fields: nothing else from meta reaches the summary', () => {
    const { summary } = describeAudit(
      row('localai.command.requested', 'localai_command', CMD, {
        nodeId: 'ai-n2',
        type: 'localai.model.load',
        params: JSON.stringify({ model_id: 'qwen3.5-9b-q4km', password: 'hunter2', token }),
        password: 'hunter2',
        authorization: `Bearer ${token}`,
      }),
    );
    expect(summary).toBe('model.load qwen3.5-9b-q4km on ai-n2');
  });

  it('scrubs free text (a gateway key name) of tokens and tailnet addresses', () => {
    const { summary } = describeAudit(row('localai.gateway.key_created', 'localai_gateway_key', CMD, { prefix: 'lai_ab12', name: `${token} at ${tailnet}` }));
    expect(summary).not.toContain(token);
    expect(summary).not.toContain(tailnet);
    expect(summary).toContain('[redacted]');
  });

  it('does not print an unknown action that carries a token in its name', () => {
    expect(describeAudit(row(`localai.x.${token}`, 'localai_x', '1', {})).summary).not.toContain(token);
  });

  it('keeps control characters and long names out of a key name', () => {
    const { summary } = describeAudit(row('localai.gateway.key_created', 'k', CMD, { name: `a\nb${'x'.repeat(200)}` }));
    expect(summary).not.toContain('\n');
    expect(summary.length).toBeLessThan(120);
  });
});

describe('auditActor', () => {
  it('shows the email of a user', () => {
    expect(auditActor('localai.command.requested', 'ai-n2', 'admin@example.test')).toEqual({ kind: 'user', label: 'admin@example.test' });
  });
  it('shows "node ai-n3" for an action a node credential performs, even though the stored actor is its owner', () => {
    for (const action of NODE_ACTOR_ACTIONS) {
      expect(auditActor(action, 'ai-n3', 'admin@example.test')).toEqual({ kind: 'node', label: 'node ai-n3' });
    }
  });
  it('shows "system" when no user is stored (a job)', () => {
    expect(auditActor('localai.command.settled', 'ai-n3', null)).toEqual({ kind: 'system', label: 'system' });
    expect(auditActor('localai.catalog.imported', null, null)).toEqual({ kind: 'system', label: 'system' });
  });
  it('falls back to the user when a node action names no node', () => {
    expect(auditActor('localai.node.enrolled', null, 'admin@example.test').kind).toBe('user');
  });
});
