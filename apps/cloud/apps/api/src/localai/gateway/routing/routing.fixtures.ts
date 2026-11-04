// Test fixtures of the routing slice: a node row that is gateway-routable by default, a model row, and the fakes the
// table and the probe are tested with. Invented ids and hosts only (`*.example.test`).
import type { SourceModelRow, SourceNodeRow } from './build-candidates';
import type { Candidate, SelectedCandidate } from './routing.types';

export const NOW = new Date('2026-03-01T12:00:00.000Z');

export function model(modelId: string, overrides: Partial<SourceModelRow> = {}): SourceModelRow {
  return { modelId, present: true, verified: true, loadable: true, status: 'loaded', slots: 2, ctx: 8192, ...overrides };
}

/** A node that passes every part of the `gatewayRoutable` filter at {@link NOW}. */
export function node(nodeId: string, models: SourceModelRow[], overrides: Partial<SourceNodeRow> = {}): SourceNodeRow {
  return {
    nodeId,
    publicHost: `${nodeId}.example.test`,
    servePattern: 'B',
    tailnetIp: null,
    tailnetMachine: null,
    gatewayPriority: 100,
    gatewayEnabled: true,
    phase: 'enrolled',
    mode: 'online',
    lastStateAt: new Date(NOW.getTime() - 5_000),
    keyReportedAt: new Date(NOW.getTime() - 3_600_000),
    eab: 'healthy',
    models,
    ...overrides,
  };
}

export function candidate(nodeId: string, overrides: Partial<SelectedCandidate> = {}): SelectedCandidate {
  const base: Candidate = {
    nodeId,
    modelId: 'model-a',
    publicHost: `${nodeId}.example.test`,
    servePattern: 'B',
    tailnetIp: null,
    tailnetMachine: null,
    priority: 100,
    status: 'loaded',
    canLoad: true,
    slots: 2,
    ctx: 8192,
    inputModalities: ['text'],
  };
  return { ...base, inflight: 0, suspectUntil: 0, ewmaTtfbMs: null, ...overrides };
}

export class FakeClock {
  constructor(private ms: number = NOW.getTime()) {}
  now(): Date {
    return new Date(this.ms);
  }
  advance(ms: number): void {
    this.ms += ms;
  }
}

/** An in-process bus: `publish` calls the handlers of the channel synchronously. */
export class FakeBus {
  readonly handlers = new Map<string, Array<(payload: unknown) => void>>();
  subscribe<T>(channel: string, handler: (payload: T) => void): () => void {
    const list = this.handlers.get(channel) ?? [];
    list.push(handler as (payload: unknown) => void);
    this.handlers.set(channel, list);
    return () => {
      this.handlers.set(channel, (this.handlers.get(channel) ?? []).filter((entry) => entry !== handler));
    };
  }
  emit(channel: string, payload: unknown): void {
    for (const handler of this.handlers.get(channel) ?? []) handler(payload);
  }
}
