// The probe's way to raise and clear the `endpoint.down` alert (docs/specs/cloud-app.md section 4.9). A port so the
// probe is tested with a fake and so a deployment without the alert engine still boots (the no-op default).
import { Injectable } from '@nestjs/common';

import { KIND_CODES, plainFix } from '../../alerts/alert-kinds';
import { AlertsService } from '../../alerts/alerts.service';

export const ENDPOINT_DOWN_KIND = 'endpoint.down';

export interface EndpointDownDetails {
  publicHost: string;
  consecutiveFailures: number;
  /** `timeout`, `connect` or `status_<code>`: a fixed vocabulary, never an error message. */
  reason: string;
}

export interface EndpointAlertsPort {
  /** Raises (or refreshes) `endpoint.down` for the node. Idempotent: the alert engine keeps one active alert per node and kind. */
  raiseDown(nodeId: string, details: EndpointDownDetails): Promise<void>;
  /** Clears `endpoint.down` for the node, if active. */
  clearDown(nodeId: string): Promise<void>;
}

/** Injection token of the {@link EndpointAlertsPort}. */
export const ENDPOINT_ALERTS = Symbol('LOCALAI_GATEWAY_ENDPOINT_ALERTS');

/** Raises nothing. For a deployment or a test without the alert engine. */
export const noopEndpointAlerts: EndpointAlertsPort = {
  async raiseDown() {},
  async clearDown() {},
};

/** The real port: `AlertsService.raiseExternal` / `clearExternal` (owned by the alert engine). */
@Injectable()
export class AlertsServiceEndpointAlerts implements EndpointAlertsPort {
  constructor(private readonly alerts: AlertsService) {}

  async raiseDown(nodeId: string, details: EndpointDownDetails): Promise<void> {
    const code = KIND_CODES[ENDPOINT_DOWN_KIND];
    await this.alerts.raiseExternal(nodeId, ENDPOINT_DOWN_KIND, {
      code,
      severity: 'warning',
      title: `${details.publicHost} is not answering (${details.consecutiveFailures} failed checks in a row)`,
      details: { ...details, fix: plainFix(code) },
    });
  }

  async clearDown(nodeId: string): Promise<void> {
    await this.alerts.clearExternal(nodeId, ENDPOINT_DOWN_KIND);
  }
}
