// The Web Push service worker (the notifications slice). Built from this file
// by `vite.config.ts` into /sw.js and registered by the notifications slice's
// `setup()`; it exists only while the slice is enabled.
//
// It does exactly what a push needs and nothing else: show the notification the
// API pushed (ALWAYS: a push that shows nothing makes Chrome print "updated in
// the background"), hand a click to an open page or open one with the
// notification's id in `?n=`, and resubscribe when the browser rotates the
// subscription. It never calls the API (it holds no token) and never caches the
// app: add precaching here (workbox) if you want an offline shell.
//
// Import the handlers from `/service-worker`, never from the `/headless` barrel:
// the barrel pulls React in, which a worker cannot load.
import {
  registerNotificationServiceWorkerHandlers,
  type NotificationsExtendableEvent,
  type NotificationsServiceWorkerScope,
} from '@marinoscar/platform-web/notifications/service-worker';

/** The members of the worker's global scope this file uses beyond the handlers'. */
type WorkerScope = NotificationsServiceWorkerScope & {
  skipWaiting(): Promise<void>;
  readonly clients: NotificationsServiceWorkerScope['clients'] & { claim(): Promise<void> };
};

const scope = self as unknown as WorkerScope;

// A new worker takes over at once, so a fixed handler reaches every open tab.
scope.addEventListener('install', () => void scope.skipWaiting());
scope.addEventListener('activate', (event: NotificationsExtendableEvent) => event.waitUntil(scope.clients.claim()));

// The favicon doubles as the badge: put PNGs under public/icons for crisper OS toasts.
registerNotificationServiceWorkerHandlers(scope, { icon: '/favicon.svg', badge: '/favicon.svg' });
