import { definePlatformHost } from '@marinoscar/platform-api/core';
import { Auth, Public } from '@marinoscar/platform-api/identity';

/**
 * The app's platform host: how a packaged controller's routes get the app's
 * access rules. Each slice's `forRoot({ host })` applies these, so a packaged
 * route is guarded exactly like one of the app's own (`@Auth()`).
 */
export const platformHost = definePlatformHost({
  access: {
    requirePermissions: (permissions) => Auth({ permissions: [...permissions] }),
    requireAuthenticated: () => Auth(),
    allowPublic: () => Public(),
  },
});
