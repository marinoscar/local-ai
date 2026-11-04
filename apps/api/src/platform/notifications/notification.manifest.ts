// =============================================================================
// The notification manifest: every channel, event and binding this app has
// =============================================================================
//
// Imported for its side effect by the notifications slice's `register()`, at
// import time and never from `onModuleInit` (several Nest applications share
// one module graph in a Jest worker, and the second would hit a frozen
// registry). The order is behaviour: `GET /api/notifications/events` lists
// events in registration order and the preferences matrix renders them so.
//
//   1. channels: the platform's (email, browser, push), then the app's;
//   2. events: identity's, the platform's (broadcasts, operations, nodes), then
//      what the other enabled slices contribute (backups),
//      the app's last.
//
// Channels before events (an event may only declare a registered channel);
// the email templates are registered by the email slice before this runs
// (a binding may only name a registered template).
// =============================================================================

import {
  BROADCASTS_NOTIFICATIONS,
  NODES_NOTIFICATIONS,
  OPS_NOTIFICATIONS,
  registerNotificationChannels,
  registerNotifications,
  registerPlatformNotificationChannels,
  type NotificationChannelDef,
} from '@marinoscar/platform-api/notifications';

import { LOCALAI_NOTIFICATIONS } from '../../localai/localai.notifications';
import { contributions } from '../slices/contributions';
import { IDENTITY_NOTIFICATIONS } from './identity.notifications';

/** The app's own channels (a webhook, SMS). Empty: the platform's three cover the alerts. */
export const APP_NOTIFICATION_CHANNELS: readonly NotificationChannelDef[] = [];

registerPlatformNotificationChannels();
registerNotificationChannels(APP_NOTIFICATION_CHANNELS);

registerNotifications(IDENTITY_NOTIFICATIONS);
registerNotifications(BROADCASTS_NOTIFICATIONS);
registerNotifications(OPS_NOTIFICATIONS);
registerNotifications(NODES_NOTIFICATIONS);
registerNotifications(contributions().notifications);
// The app's own events are registered here, after the platform's.
registerNotifications(LOCALAI_NOTIFICATIONS);
