// The Local AI notification events (docs/specs/cloud-app.md section 4.9), registered through the notification manifest
// (`src/platform/notifications/notification.manifest.ts` calls `registerNotifications(LOCALAI_NOTIFICATIONS)`).
//
// EVENT KEYS ARE PERSISTED (user preferences, delivery rows): never rename one, add a new one.
//
// KEY SHAPE. The platform registry accepts `<area>.<event>`: ONE dot, lower snake case (`NOTIFICATION_EVENT_KEY_PATTERN`,
// like its own `nodes.node_offline`). The spec's first draft wrote `localai.alert.raised`, which the registry refuses, so
// the permanent keys are `localai.alert_raised`, `localai.alert_cleared` and `localai.node_enrolled` (spec section 4.9,
// "As built"). The EVENT BUS channel `localai.alert.changed` and the AUDIT actions are not notification keys and keep
// their dotted names.
//
// CHANNELS. `localai.alert_raised` declares browser and email; whether a given alert also mails is decided per dispatch
// (`alerts-notifier.ts` narrows the channels to browser alone unless the alert is critical), and a user's own
// preferences can still mute either channel. Recipients are the holders of `lab:operate`; `localai.node_enrolled` goes
// to the holders of `lab:admin`.
//
// `localai.command_failed` (the requester, browser) belongs to the command API issue (#83) and is added there.
import type { BrowserNotificationTemplate, NotificationRegistration } from '@marinoscar/platform-api/notifications';

import type { AlertNotice } from './alerts/alert-ports';

export const NOTIFICATION_ALERT_RAISED = 'localai.alert_raised';
export const NOTIFICATION_ALERT_CLEARED = 'localai.alert_cleared';
export const NOTIFICATION_NODE_ENROLLED = 'localai.node_enrolled';

/** The page every lab notification opens (a root-relative path, as the browser channel requires). */
export const ALERTS_PAGE_LINK = '/lab/alerts';

const oneLine = (value: string): string => value.replace(/[\u0000-\u001f\u007f]+/g, ' ').replace(/\s+/g, ' ').trim();

export const alertRaisedBrowser: BrowserNotificationTemplate = (data: AlertNotice) => ({
  title: `${oneLine(data.node)}: ${oneLine(data.title)}`,
  body: oneLine(data.fix),
  link: ALERTS_PAGE_LINK,
});

export const alertClearedBrowser: BrowserNotificationTemplate = (data: AlertNotice) => ({
  title: `${oneLine(data.node)}: ${oneLine(data.title)} is resolved`,
  body: 'No action is needed.',
  link: ALERTS_PAGE_LINK,
});

export const nodeEnrolledBrowser: BrowserNotificationTemplate = (data: { node: string; displayName: string }) => ({
  title: `${oneLine(data.node)} joined the lab`,
  body: `${oneLine(data.displayName)} contacted the cloud for the first time.`,
  link: `/lab/nodes/${encodeURIComponent(data.node)}`,
});

export const LOCALAI_NOTIFICATIONS: readonly NotificationRegistration[] = [
  {
    event: {
      key: NOTIFICATION_ALERT_RAISED,
      label: 'Lab alert',
      description: 'Sent when a node or the ark needs attention: a node stopped reporting, the AI-ARK drive is overdue, a sync or the monthly drill failed. Critical alerts are also mailed.',
      channels: ['browser', 'email'],
      defaultEnabled: true,
    },
    emailTemplate: 'localai-alert-raised',
    browserTemplate: alertRaisedBrowser,
  },
  {
    event: {
      key: NOTIFICATION_ALERT_CLEARED,
      label: 'Lab alert resolved',
      description: 'Sent when an alert you were told about clears by itself.',
      channels: ['browser'],
      defaultEnabled: true,
    },
    browserTemplate: alertClearedBrowser,
  },
  {
    event: {
      key: NOTIFICATION_NODE_ENROLLED,
      label: 'A node joined the lab',
      description: 'Sent to lab administrators when a new node contacts the cloud with its credential for the first time.',
      channels: ['browser', 'email'],
      defaultEnabled: true,
    },
    emailTemplate: 'localai-node-enrolled',
    browserTemplate: nodeEnrolledBrowser,
  },
];
