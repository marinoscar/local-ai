// The notifications slice, on the web: the bell in the top bar with its live
// inbox (one SSE connection per tab), the per-user preferences page, the
// admin policy, Web Push key and broadcast pages, and the permission banner.
//
// WEB PUSH needs the service worker built from `src/sw.ts` (vite.config.ts
// emits it at /sw.js) and registered by `main.tsx`; it works over HTTPS or on
// localhost, and only after an administrator generates a key pair at
// /admin/settings/push (stored encrypted at runtime: no environment variable).
import CampaignOutlinedIcon from '@mui/icons-material/CampaignOutlined';
import NotificationsActiveOutlinedIcon from '@mui/icons-material/NotificationsActiveOutlined';
import NotificationsIcon from '@mui/icons-material/Notifications';
import VpnKeyOutlinedIcon from '@mui/icons-material/VpnKeyOutlined';
import { connectSse } from '@marinoscar/platform-web/core';
import {
  NotificationProvider,
  configureNotificationsWeb,
  removePushSubscription,
  usePushSubscriptionSync,
} from '@marinoscar/platform-web/notifications/headless';
import { NotificationBell, NotificationPermissionBanner } from '@marinoscar/platform-web/notifications/ui';
import { lazy } from 'react';

import { api } from '../api';
import type { WebSlice } from './slice';

const UserNotificationsPage = lazy(() => import('@marinoscar/platform-web/notifications/ui').then((m) => ({ default: m.UserNotificationsPage })));
const NotificationSettingsPage = lazy(() => import('@marinoscar/platform-web/notifications/ui').then((m) => ({ default: m.NotificationSettingsPage })));
const PushConfigPage = lazy(() => import('@marinoscar/platform-web/notifications/ui').then((m) => ({ default: m.PushConfigPage })));
const BroadcastsPage = lazy(() => import('@marinoscar/platform-web/notifications/ui').then((m) => ({ default: m.BroadcastsPage })));

/** Asks this device for notification permission (or says why it cannot be granted); renders nothing otherwise. */
function PermissionBanner() {
  const pushSync = usePushSubscriptionSync();
  return (
    <NotificationPermissionBanner
      config={pushSync.config}
      capability={pushSync.capability}
      onRequestPermission={() => void pushSync.requestPermission()}
      isRequestingPermission={pushSync.isRequestingPermission}
    />
  );
}

export const notificationsWebSlice: WebSlice = {
  id: 'notifications',
  setup: () => {
    // The slice's services are plain module functions (logout drops the push
    // subscription through one, outside React), so they reach the API through
    // this one configured client: the same bearer token, refresh and retries.
    configureNotificationsWeb({ api, apiBaseUrl: import.meta.env.VITE_API_BASE_URL || '/api', connectSse });
    // The worker that shows pushes (src/sw.ts, served at /sw.js). Registering is harmless
    // until an administrator generates a key pair; a browser without workers skips it.
    if (typeof navigator !== 'undefined' && 'serviceWorker' in navigator) {
      navigator.serviceWorker.register('/sw.js').catch(() => undefined);
    }
  },
  beforeLogout: removePushSubscription,
  // Inside the sign-in gate, around the shell: ONE inbox and ONE stream per tab.
  shellProviders: [NotificationProvider],
  appBarActions: [NotificationBell],
  banners: [PermissionBanner],
  routes: [
    // Any signed-in user may edit their own preferences.
    { path: 'settings/notifications', element: <UserNotificationsPage /> },
    { path: 'admin/settings/notifications', permission: 'system_settings:read', element: <NotificationSettingsPage /> },
    { path: 'admin/settings/push', permission: 'push:read', element: <PushConfigPage /> },
    // Either string admits: the system one (every user) or the org one (that organization's members).
    { path: 'admin/settings/broadcasts', permission: ['broadcasts:read', 'org_broadcasts:read'], element: <BroadcastsPage /> },
  ],
  adminCards: [
    {
      group: 'General',
      cards: [
        {
          title: 'Notifications',
          description: 'Turn browser notifications on or off for everyone, and suppress individual events.',
          Icon: NotificationsActiveOutlinedIcon,
          path: '/admin/settings/notifications',
          permission: 'system_settings:read',
        },
        {
          title: 'Web Push',
          description: 'Generate a VAPID key pair, enable or rotate it, and control whether this deployment can send browser push notifications.',
          Icon: VpnKeyOutlinedIcon,
          path: '/admin/settings/push',
          permission: 'push:read',
        },
      ],
    },
    {
      group: 'Operations',
      cards: [
        {
          title: 'Broadcasts',
          description: 'Write an announcement and send it to every active user now or at a scheduled time, then watch it go out.',
          Icon: CampaignOutlinedIcon,
          path: '/admin/settings/broadcasts',
          permission: ['broadcasts:read', 'org_broadcasts:read'],
        },
      ],
    },
  ],
  userCards: [
    {
      group: 'Account',
      cards: [
        {
          title: 'Notifications',
          description: 'Choose which events notify you, and whether they arrive by email or in your browser.',
          Icon: NotificationsIcon,
          path: '/settings/notifications',
        },
      ],
    },
  ],
  consolePermissions: ['push:read', 'broadcasts:read'],
};
