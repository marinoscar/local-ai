// The notifications slice, configured once: the dispatcher, the channels, the
// SSE stream, `/api/notifications`, `/api/admin/push-config`, the Web Push
// (VAPID) Doctor check and the job-failure and node-offline notifiers; global.
// `BroadcastsModule` is the package's static admin-broadcast module (needs the
// jobs slice, which the starter always mounts).
//
// The notification registrations (`./notification.manifest.ts`) must have run
// before this file is required: `slices/register.ts` does, before any module.
import {
  BroadcastsModule as PlatformBroadcastsModule,
  NotificationsModule as PlatformNotificationsModule,
} from '@marinoscar/platform-api/notifications';

import { EmailModule } from '../email/email.config';
import { NotificationsHostModule } from './notifications-host.module';

export const NotificationsModule = PlatformNotificationsModule.forRoot({
  imports: [EmailModule, NotificationsHostModule],
});

export const BroadcastsModule = PlatformBroadcastsModule;
