// The notifications slice: the in-app inbox and bell, per-user preferences,
// email delivery, Web Push, admin broadcasts, and the job-failure and
// node-offline notices. Turns the identity notices from log lines into mail.
// Web Push needs the web app's service worker (`apps/web/src/sw.ts`) and an
// administrator-generated VAPID key pair (`/admin/settings/push`): no environment variable.
import type { SystemSettingsNamespace, UserSettingsNamespace } from '@marinoscar/platform-api/settings';

import type { ApiSlice } from '../slices/slice';

export const notificationsSlice: ApiSlice = {
  id: 'notifications',
  permissionSlices: ['notifications'],
  contribute: () => {
    const notifications = require('@marinoscar/platform-api/notifications') as typeof import('@marinoscar/platform-api/notifications');
    return {
      systemSettings: [notifications.NOTIFICATIONS_SYSTEM_SETTINGS as SystemSettingsNamespace],
      userSettings: [notifications.NOTIFICATIONS_USER_SETTINGS as UserSettingsNamespace],
      credentialPurposes: [notifications.PUSH_VAPID_CREDENTIAL_PURPOSE_DEF],
    };
  },
  register: () => {
    require('./notification.manifest');
  },
  modules: () => {
    const config = require('./notifications.config') as typeof import('./notifications.config');
    return [config.NotificationsModule, config.BroadcastsModule];
  },
  hostPorts: () => {
    const { NotificationsIdentityNotifier } = require('./identity-notifier.adapter') as typeof import('./identity-notifier.adapter');
    const { IDENTITY_NOTIFIER } = require('@marinoscar/platform-api/identity') as typeof import('@marinoscar/platform-api/identity');
    return [{ provide: IDENTITY_NOTIFIER, useClass: NotificationsIdentityNotifier }];
  },
};
