// The wire shapes of the Add-node wizard routes (docs/specs/cloud-app.md sections 3.9 and 4.8). Mirrors
// apps/api/src/localai/wizard/{wizard.service,checklist.service}.ts; the web tests render the same shapes.

export type ServePattern = 'A' | 'B';

export interface CreateNodeInput {
  nodeId: string;
  displayName?: string;
  publicHost?: string;
  servePattern: ServePattern;
  tailnetMachine?: string;
  tailnetIp?: string;
  profile?: string;
}

export interface InstallInfo {
  /** The three commands, one per line; null while no agent release is configured. */
  oneLiner: string | null;
  pinnedScriptUrl: string | null;
  /** 64 hex, or null when unknown. */
  scriptSha256: string | null;
  note: string | null;
  releasePageUrl: string | null;
}

export interface CreateNodeResult {
  node: { nodeId: string; displayName: string; publicHost: string; servePattern: ServePattern; phase: 'pending' };
  /** SHOWN ONCE: this response is the only copy of `token`. */
  credential: { token: string; prefix: string };
  install: InstallInfo;
}

export type ChecklistStepId = 'tailnet' | 'dns' | 'bootstrap_vhost' | 'certificate' | 'https_vhost' | 'enrolled' | 'key' | 'gateway_test';
export type StepState = 'todo' | 'done' | 'failed';
export type StepKind = 'manual' | 'check' | 'auto';

export interface Snippet {
  title: string;
  language: 'bash' | 'nginx' | 'text';
  path?: string;
  content: string;
}

export interface ChecklistStep {
  step: ChecklistStepId;
  order: number;
  kind: StepKind;
  state: StepState;
  checkedAt: string | null;
  detail: string | null;
  title: string;
  snippets: Snippet[];
  missing: Array<'tailnetIp' | 'tailnetName'>;
}

export interface Checklist {
  nodeId: string;
  publicHost: string;
  servePattern: 'A' | 'B' | 'none';
  complete: boolean;
  steps: ChecklistStep[];
}

/** What the wizard reads from `GET /api/localai/nodes/:nodeId` while it waits for the node. */
export interface NodeStatus {
  nodeId: string;
  phase: 'pending' | 'enrolled' | 'retired';
  keyReceived: boolean;
}

export type ChecklistAction = 'mark_done' | 'mark_todo' | 'check';
