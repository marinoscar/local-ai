import { useMemo, type ReactNode } from 'react';
import { PlatformHostProvider, type PlatformWebHost } from '@marinoscar/platform-web/core';
import { useAuth, usePermissions } from '@marinoscar/platform-web/identity/headless';

import { platformApi } from './api';
import { useSliceFeatures } from './slices/manifest';

/** The host every packaged page reads: the transport, who is looking, what they may do. */
export function AppPlatformHost({ children }: { children: ReactNode }) {
  const { user, refreshUser } = useAuth();
  const { hasPermission } = usePermissions();
  // The features the enabled slices expose (`ai`); a fixed set of hooks, since the enabled slices never change at run time.
  const sliceFeatures = useSliceFeatures();
  const sliceFeatureKey = JSON.stringify(sliceFeatures);
  const features = useMemo<Record<string, boolean>>(
    () => ({ ...(JSON.parse(sliceFeatureKey) as Record<string, boolean>), orgs: user?.tenancyMode === 'multi' }),
    [sliceFeatureKey, user?.tenancyMode],
  );
  const host = useMemo<PlatformWebHost>(
    () => ({
      api: platformApi,
      viewer: {
        userId: user?.id ?? null,
        email: user?.email ?? null,
        hasPermission,
        isFeatureEnabled: (feature) => features[feature] === true,
        // The Danger Zone and the factory reset re-read the user after they succeed.
        refresh: refreshUser,
      },
    }),
    [user?.id, user?.email, hasPermission, refreshUser, features],
  );
  return <PlatformHostProvider host={host}>{children}</PlatformHostProvider>;
}
