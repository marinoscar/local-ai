// Test data for the wizard: the shapes the API returns (apps/api/src/localai/wizard). The node credential is built at
// run time, never a literal, so no token-shaped string sits in the repository.
import type { Checklist, ChecklistStep, ChecklistStepId, CreateNodeResult, NodeStatus, StepKind, StepState } from './wizardTypes';
import type { WizardClient } from './wizardApi';

export const NODE_ID = 'ai-n4';

/** `nod_` and 64 hex characters, different on every call. */
export const makeToken = (): string => `nod_${Array.from(crypto.getRandomValues(new Uint8Array(32)), (b) => b.toString(16).padStart(2, '0')).join('')}`;

export const SHA = 'ab'.repeat(32);

export function createResult(token: string = makeToken(), patch: Partial<CreateNodeResult['install']> = {}): CreateNodeResult {
  return {
    node: { nodeId: NODE_ID, displayName: 'Dell Precision (ai-n4)', publicHost: 'ai-n4.marin.cr', servePattern: 'B', phase: 'pending' },
    credential: { token, prefix: token.slice(0, 8) },
    install: {
      oneLiner: [
        'curl -fsSLO https://github.com/marinoscar/local-ai/releases/download/node-v0.4.1/install-node.sh',
        'sha256sum install-node.sh          # compare with the checksum shown here',
        `sudo bash install-node.sh --node-id ${NODE_ID} --cloud-url https://ai.marin.cr --enroll`,
      ].join('\n'),
      pinnedScriptUrl: 'https://github.com/marinoscar/local-ai/releases/download/node-v0.4.1/install-node.sh',
      scriptSha256: SHA,
      note: null,
      releasePageUrl: 'https://github.com/marinoscar/local-ai/releases/tag/node-v0.4.1',
      ...patch,
    },
  };
}

const TITLES: Record<ChecklistStepId, string> = {
  tailnet: 'Node is on the tailnet (tailscale up)',
  dns: 'DNS: ai-n4.marin.cr A → VPS',
  bootstrap_vhost: 'Bootstrap HTTP vhost',
  certificate: 'Certificate issued',
  https_vhost: 'Final HTTPS vhost (Pattern B)',
  enrolled: 'Node enrolled',
  key: 'llama key received by the cloud',
  gateway_test: 'Gateway test: /v1/models through ai-n4',
};
const KINDS: Record<ChecklistStepId, StepKind> = { tailnet: 'manual', dns: 'check', bootstrap_vhost: 'manual', certificate: 'check', https_vhost: 'check', enrolled: 'auto', key: 'auto', gateway_test: 'check' };
const ORDER = Object.keys(TITLES) as ChecklistStepId[];

export function checklist(states: Partial<Record<ChecklistStepId, StepState>> = {}, details: Partial<Record<ChecklistStepId, string>> = {}): Checklist {
  const steps: ChecklistStep[] = ORDER.map((step, index) => ({
    step,
    order: index + 1,
    kind: KINDS[step],
    state: states[step] ?? 'todo',
    checkedAt: null,
    detail: details[step] ?? null,
    title: TITLES[step],
    snippets:
      step === 'https_vhost'
        ? [{ title: 'Final HTTPS vhost (Pattern B)', language: 'nginx', path: '/opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf', content: 'server {\n    server_name ai-n4.marin.cr;\n}\n' }]
        : step === 'dns'
          ? [{ title: 'Check from any computer', language: 'bash', content: 'dig +short ai-n4.marin.cr' }]
          : [],
    missing: step === 'https_vhost' ? ['tailnetIp'] : [],
  }));
  return { nodeId: NODE_ID, publicHost: 'ai-n4.marin.cr', servePattern: 'B', complete: steps.every((s) => s.state === 'done'), steps };
}

export const ALL_DONE: Partial<Record<ChecklistStepId, StepState>> = Object.fromEntries(ORDER.map((s) => [s, 'done' as StepState]));

/** A fake transport. Every call is a `vi.fn` so a test can script and inspect it. */
export function fakeClient(patch: Partial<{ [K in keyof WizardClient]: ReturnType<typeof vi.fn> }> = {}): WizardClient & { [K in keyof WizardClient]: ReturnType<typeof vi.fn> } {
  const status: NodeStatus = { nodeId: NODE_ID, phase: 'pending', keyReceived: false };
  return {
    nextNodeId: vi.fn().mockResolvedValue(NODE_ID),
    createNode: vi.fn().mockImplementation(async () => createResult()),
    nodeStatus: vi.fn().mockResolvedValue(status),
    checklist: vi.fn().mockResolvedValue(checklist()),
    act: vi.fn().mockResolvedValue(checklist()),
    ...patch,
  } as never;
}
