// The two outward ports of the alert engine: the notifications it sends and the event it publishes. Both are tokens so
// the engine is testable with fakes and so a deployment without the notifications slice still boots (the real notifier
// takes the dispatcher `@Optional()`).
import type { AlertSeverity } from './alert-kinds';

/** What a notification about an alert says (the payload handed to the dispatcher; also what the templates render). */
export interface AlertNotice {
  alertId: string;
  /** The lab node id (`ai-n2`), or "Lab" for an alert that belongs to no node. */
  node: string;
  kind: string;
  code: string;
  severity: AlertSeverity;
  title: string;
  /** The plain-language fix. */
  fix: string;
}

export interface AlertNotifier {
  /** `localai.alert.raised`: a new alert, an escalation or a repeat. Browser for every severity, email only for `critical`. */
  raised(notice: AlertNotice): Promise<void>;
  /** `localai.alert.cleared`: the alert is resolved. Browser only. */
  cleared(notice: AlertNotice): Promise<void>;
}

/** Injection token of the {@link AlertNotifier}. */
export const LOCALAI_ALERT_NOTIFIER = Symbol('LOCALAI_ALERT_NOTIFIER');

/** The event bus channel (spec section 7); the payload is {@link AlertChangedEvent}. */
export const ALERT_CHANGED_CHANNEL = 'localai.alert.changed';

export interface AlertChangedEvent {
  alertId: string;
  /** The lab node id, or null for a lab-wide alert. */
  nodeId: string | null;
  kind: string;
  state: 'active' | 'cleared';
}
