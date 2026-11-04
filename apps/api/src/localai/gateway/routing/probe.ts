// The endpoint health probe (docs/specs/gateway.md section 4.3).
//
// Every 30 s, for each node the routing table admits: `GET https://<publicHost>/health`, no key, 3 s timeout. It uses
// the PUBLIC host only (never the tailnet address, never a key), so it works in `tailnet` upstream mode too.
//
//   success               failures = 0; if the node was suspect: not suspect any more; if `endpoint.down` is active: cleared
//   failure               failures + 1; the third in a row makes the node suspect until its next success and raises
//                         `endpoint.down` (LAI-CLD-027) ONCE (the alert engine keeps one active alert per node and kind)
//
// THE REQUEST PATH NEVER WAITS ON IT. The probe owns its timer and its sockets; the table's `select` reads the suspect
// flag from memory. One run at a time: a run that is still going when the next tick fires makes the tick a no-op.
// The probe logs the node id and a fixed reason (`timeout`, `connect`, `status_<code>`), never an error message.
import { Inject, Injectable, Logger, type OnApplicationBootstrap, type OnModuleDestroy } from '@nestjs/common';

import { ENDPOINT_ALERTS, type EndpointAlertsPort } from './endpoint-alerts.port';
import { ROUTING_OPTIONS, RoutingTable, type RoutingOptions } from './routing-table';
import { PROBE_FAILURES_TO_DOWN, PROBE_INTERVAL_MS, PROBE_TIMEOUT_MS } from './routing.types';

export type ProbeResult = { ok: true } | { ok: false; reason: string };

/** The HTTP side of the probe, a port so the state machine is tested without a network. */
export interface HealthClient {
  check(url: string, timeoutMs: number): Promise<ProbeResult>;
}

/** Injection token of the {@link HealthClient}. */
export const HEALTH_CLIENT = Symbol('LOCALAI_GATEWAY_HEALTH_CLIENT');

/** `fetch` with a hard timeout. A 2xx is healthy; a redirect is not followed and counts as a failure. */
export const fetchHealthClient: HealthClient = {
  async check(url, timeoutMs) {
    try {
      const response = await fetch(url, { method: 'GET', redirect: 'manual', signal: AbortSignal.timeout(timeoutMs), headers: { accept: 'application/json' } });
      // Do not read the body: only the status matters, and a body of unknown size is not ours to buffer.
      await response.body?.cancel().catch(() => undefined);
      return response.status >= 200 && response.status < 300 ? { ok: true } : { ok: false, reason: `status_${response.status}` };
    } catch (error) {
      const name = error instanceof Error ? error.name : '';
      return { ok: false, reason: name === 'TimeoutError' || name === 'AbortError' ? 'timeout' : 'connect' };
    }
  },
};

interface NodeProbeState {
  failures: number;
  /** The node is suspect because of the probe. */
  down: boolean;
  /** `endpoint.down` was raised by this process and not cleared since. */
  alertRaised: boolean;
  /** An `endpoint.down` alert left by a previous process was looked for and cleared (done on the first success). */
  clearChecked: boolean;
}

@Injectable()
export class EndpointProbe implements OnApplicationBootstrap, OnModuleDestroy {
  private readonly logger = new Logger(EndpointProbe.name);
  private readonly states = new Map<string, NodeProbeState>();
  private interval: ReturnType<typeof setInterval> | null = null;
  private running: Promise<void> | null = null;
  private stopped = false;

  constructor(
    private readonly table: RoutingTable,
    @Inject(HEALTH_CLIENT) private readonly client: HealthClient,
    @Inject(ENDPOINT_ALERTS) private readonly alerts: EndpointAlertsPort,
    @Inject(ROUTING_OPTIONS) private readonly options: RoutingOptions,
  ) {}

  onApplicationBootstrap(): void {
    if (!this.options.timers) return;
    this.interval = setInterval(() => void this.runOnce(), PROBE_INTERVAL_MS);
    this.interval.unref?.();
  }

  onModuleDestroy(): void {
    this.stopped = true;
    if (this.interval) clearInterval(this.interval);
    this.interval = null;
  }

  /** One pass over the routable nodes. Never rejects; a pass already running makes this one a no-op. */
  runOnce(): Promise<void> {
    if (this.stopped) return Promise.resolve();
    if (this.running) return this.running;
    this.running = this.pass().finally(() => {
      this.running = null;
    });
    return this.running;
  }

  private async pass(): Promise<void> {
    const nodes = this.table.routableNodes();
    const ids = new Set(nodes.map((node) => node.nodeId));
    await Promise.all(
      nodes.map(async (node) => {
        const result = await this.client.check(`https://${node.publicHost}/health`, PROBE_TIMEOUT_MS);
        await this.apply(node.nodeId, node.publicHost, result);
      }),
    );
    // A node that left the table (offline mode, survival, retired, key gone) is expected to be silent: forget it and
    // lift what the probe set. Its own alerts (node.offline and the like) say what is wrong.
    for (const nodeId of [...this.states.keys()]) if (!ids.has(nodeId)) await this.forget(nodeId);
  }

  /** The state machine for one result. Exposed for the unit test. */
  async apply(nodeId: string, publicHost: string, result: ProbeResult): Promise<void> {
    const state = this.stateOf(nodeId);
    if (result.ok) {
      state.failures = 0;
      if (state.down) {
        state.down = false;
        this.table.setProbeSuspect(nodeId, false);
        this.logger.log(`endpoint probe recovered: node_id=${nodeId}`);
      }
      if (state.alertRaised || !state.clearChecked) await this.clearAlert(nodeId, state);
      return;
    }
    state.failures += 1;
    this.logger.debug(`endpoint probe failed: node_id=${nodeId} failures=${state.failures} reason=${result.reason}`);
    if (state.failures < PROBE_FAILURES_TO_DOWN) return;
    if (!state.down) {
      state.down = true;
      this.table.setProbeSuspect(nodeId, true);
      this.logger.warn(`endpoint probe: node_id=${nodeId} failed ${state.failures} checks in a row, marked suspect (reason=${result.reason})`);
    }
    if (!state.alertRaised) {
      try {
        await this.alerts.raiseDown(nodeId, { publicHost, consecutiveFailures: state.failures, reason: result.reason });
        state.alertRaised = true;
      } catch (error) {
        // Not marked raised: the next failed check tries again.
        this.logger.error(`raising endpoint.down failed for node_id=${nodeId} (${error instanceof Error ? error.name : 'error'})`);
      }
    }
  }

  private async clearAlert(nodeId: string, state: NodeProbeState): Promise<void> {
    try {
      await this.alerts.clearDown(nodeId);
      state.alertRaised = false;
      state.clearChecked = true;
    } catch (error) {
      this.logger.error(`clearing endpoint.down failed for node_id=${nodeId} (${error instanceof Error ? error.name : 'error'})`);
    }
  }

  private async forget(nodeId: string): Promise<void> {
    const state = this.states.get(nodeId);
    this.states.delete(nodeId);
    this.table.setProbeSuspect(nodeId, false);
    if (state?.alertRaised) await this.clearAlert(nodeId, state);
  }

  private stateOf(nodeId: string): NodeProbeState {
    let state = this.states.get(nodeId);
    if (!state) {
      state = { failures: 0, down: false, alertRaised: false, clearChecked: false };
      this.states.set(nodeId, state);
    }
    return state;
  }
}
