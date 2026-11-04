// Test fixtures of the models slice: an approved catalog (`models.yaml` text), policies, and the "fixture lab" of three
// nodes the acceptance criteria of GW-05 describe. Invented ids and hosts only (`*.example.test`).
import type { SourceNodeRow } from '../routing/build-candidates';
import { FakeBus, FakeClock, model, node } from '../routing/routing.fixtures';
import type { RoutingSource } from '../routing/routing-source';
import { RoutingTable } from '../routing/routing-table';
import { ApprovedCatalogReader, parseModelsYaml, type ApprovedCatalog, type ApprovedCatalogStore } from './catalog-hints';
import { DEFAULT_MODEL_POLICY, type ModelPolicy, type ModelPolicySource } from './policy';
import { buildDirectory, type ModelDirectory } from './resolve';

/** 2026-11-02T12:00:00Z, the approval time of the fixture catalog. */
export const APPROVED_AT = new Date('2026-11-02T12:00:00.000Z');
export const APPROVED_SECONDS = 1793620800;

/** The `models.yaml` of the fixture catalog: five llama models and a whisper model. */
export const MODELS_YAML = `schema_version: 1
models:
  - id: qwen3.5-9b-q4km
    display_name: "Qwen 3.5 9B"
    summary: "Fast general assistant."
    engine: llama
    roles: [chat, reasoning, coder]
    llama:
      ctx: 65536
      parallel: 1
    client_hints:
      max_output_tokens: 8192
      reasoning: true
  - id: qwen3.6-35b-a3b-q4km
    display_name: "Qwen 3.6 35B A3B"
    engine: llama
    roles: [chat, reasoning]
    llama:
      ctx: 65536
    client_hints:
      max_output_tokens: 8192
      reasoning: true
  - id: qwen3-coder-next-q4km
    display_name: "Qwen3 Coder Next"
    engine: llama
    roles: [coder, chat]
    llama:
      ctx: 131072
    client_hints:
      max_output_tokens: 16384
      reasoning: false
  - id: qwen3.5-4b-q4km
    display_name: "Qwen 3.5 4B"
    engine: llama
    roles: [chat]
    llama:
      ctx: 32768
    client_hints:
      max_output_tokens: 4096
      reasoning: false
  - id: catalog-only-model
    display_name: "Catalog only"
    engine: llama
    roles: [chat]
    llama:
      ctx: 8192
  - id: whisper-small
    display_name: "Whisper Small"
    engine: whisper
    roles: [stt]
`;

export function catalogOf(yaml: string = MODELS_YAML, approvedAt: Date = APPROVED_AT, version = '2026.11.02-1'): ApprovedCatalog {
  return { version, approvedAt, createdSeconds: Math.floor(approvedAt.getTime() / 1000), models: parseModelsYaml(yaml) };
}

export function policy(overrides: Partial<ModelPolicy> = {}): ModelPolicy {
  return { ...DEFAULT_MODEL_POLICY, ...overrides };
}

/** Policies of the fixture lab: the alias `lab-coder` and on-demand load on the 35B model. */
export function labPolicies(): Map<string, ModelPolicy> {
  return new Map([
    ['qwen3-coder-next-q4km', policy({ aliases: ['lab-coder'] })],
    ['qwen3.6-35b-a3b-q4km', policy({ onDemandLoad: true, loadWaitS: 120 })],
  ]);
}

/** The three nodes of the fixture lab. ai-n1 serves the coder, ai-n2 can load the 35B, ai-n3 serves the 9B. */
export function labNodes(): SourceNodeRow[] {
  return [
    node('ai-n1', [model('qwen3-coder-next-q4km', { ctx: 131072 }), model('whisper-small', { status: 'unloaded', loadable: false })]),
    node('ai-n2', [model('qwen3.6-35b-a3b-q4km', { status: 'unloaded', ctx: 32768 }), model('qwen3.5-4b-q4km', { status: 'unloaded', ctx: 32768 })]),
    node('ai-n3', [model('qwen3.5-9b-q4km', { ctx: 32768, inputModalities: ['text'] })]),
  ];
}

export class FakeRowsSource implements RoutingSource {
  constructor(public rows: SourceNodeRow[]) {}
  async load() {
    return { rows: this.rows.map((row) => ({ ...row })), settings: { queueFactor: 4 } };
  }
}

/** A routing table over `rows`, rebuilt once. */
export async function tableOf(rows: SourceNodeRow[]): Promise<{ table: RoutingTable; source: FakeRowsSource; clock: FakeClock }> {
  const source = new FakeRowsSource(rows);
  const clock = new FakeClock();
  const table = new RoutingTable(source, new FakeBus() as never, clock, { timers: false, rebuildIntervalMs: 10_000 });
  table.onModuleInit();
  await table.rebuild();
  return { table, source, clock };
}

export function directoryOf(policies: Map<string, ModelPolicy> = labPolicies(), catalog: ApprovedCatalog | null = catalogOf()): ModelDirectory {
  return buildDirectory(catalog, policies);
}

export class FakeCatalogStore implements ApprovedCatalogStore {
  heads = 0;
  yamls = 0;
  fail: Error | null = null;
  constructor(
    public head_: { version: string; approvedAt: Date } | null = { version: '2026.11.02-1', approvedAt: APPROVED_AT },
    public yaml: string | null = MODELS_YAML,
  ) {}
  async head() {
    this.heads += 1;
    if (this.fail) throw this.fail;
    return this.head_;
  }
  async modelsYaml() {
    this.yamls += 1;
    return this.yaml;
  }
}

export class FakePolicySource implements ModelPolicySource {
  reads = 0;
  constructor(public policies: Map<string, ModelPolicy> = labPolicies()) {}
  fail: Error | null = null;
  async all() {
    this.reads += 1;
    if (this.fail) throw this.fail;
    return new Map(this.policies);
  }
}

export function readerOf(store: FakeCatalogStore, clock: FakeClock): ApprovedCatalogReader {
  return new ApprovedCatalogReader(store, clock);
}
